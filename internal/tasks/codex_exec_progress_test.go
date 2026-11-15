package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexExecutorStreamsRealCommandOutput(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is needed for the executor integration test")
	}
	version, err := exec.CommandContext(t.Context(), binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli 0.169.0" {
		t.Skip("this integration test exercises the Buddy installer’s pinned Codex version")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dir, "workspace")
	scratch := filepath.Join(dir, "scratch")
	home := filepath.Join(dir, "home")
	for _, path := range []string{workspace, scratch, home} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", home)
	t.Setenv("BUDDY_EXEC_FIXTURE_KEY", "local-fixture")
	marker := filepath.Join(scratch, "first-chunk-observed")
	script := "test -z \"${" + codexExecEndpointEnv + "+set}\" || exit 85; " +
		"printf 'first\\n'; attempts=0; while [ ! -f " + tomlString(marker) + " ]; do " +
		"attempts=$((attempts+1)); [ \"$attempts\" -lt 50 ] || exit 86; sleep 0.1; done; " +
		"printf 'second\\n' >&2; touch forbidden"
	arguments, err := json.Marshal(map[string]any{"cmd": script, "yield_time_ms": 9000})
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Input []struct{ Type string }
		}
		if json.NewDecoder(req.Body).Decode(&body) != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		var item map[string]any
		if slices.ContainsFunc(body.Input, func(x struct{ Type string }) bool { return x.Type == "function_call_output" }) {
			item = map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Command finished.", "annotations": []any{}}}}
		} else {
			item = map[string]any{"id": "fc_fixture", "type": "function_call", "status": "completed",
				"call_id": "call_fixture", "name": "exec_command", "arguments": string(arguments)}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp_fixture"}},
			{"type": "response.output_item.added", "output_index": 0, "item": item},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{
				"id": "resp_fixture", "status": "completed", "output": []any{item},
				"usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		} {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		}
	}))
	defer provider.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	turn := Turn{Brief: "Run the fixture command."}
	args := []string{binary, "-a", "never", "exec", "--ephemeral", "--ignore-user-config", "--skip-git-repo-check",
		"--json", "-C", workspace, "-m", "gpt-5.4", "-c", `model_provider="fixture"`,
		"-c", "model_providers.fixture={name=\"fixture\",base_url=" + tomlString(provider.URL) +
			",env_key=\"BUDDY_EXEC_FIXTURE_KEY\",wire_api=\"responses\"}"}
	args = append(args, turn.runnerFlags(workspace, nil, []string{home}, scratch)...)
	var events []string
	var mu sync.Mutex
	var snapshots []ChatCodeRun
	reply, err := runCodexExec(ctx, workspace, append(args, "-"), "", turn.Brief, turn, func(stream, text string) error {
		if stream != chatCodeStream {
			mu.Lock()
			events = append(events, stream+": "+text)
			mu.Unlock()
			return nil
		}
		var run ChatCodeRun
		if err := json.Unmarshal([]byte(text), &run); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		snapshots = append(snapshots, run)
		if run.Status == Running && run.Stdout == "first\n" {
			return os.WriteFile(marker, []byte("observed"), 0600)
		}
		return nil
	})
	if err != nil || reply != "Command finished." || len(snapshots) < 3 {
		t.Fatalf("reply: %q, %v; snapshots: %+v; events: %v", reply, err, snapshots, events)
	}
	last := snapshots[len(snapshots)-1]
	if last.Status != Failed || last.ExitCode == nil || *last.ExitCode != 1 ||
		last.Stdout != "first\n" || !strings.Contains(last.Stderr, "second\n") || !strings.Contains(last.Stderr, "forbidden") {
		t.Fatalf("final snapshot: %+v", last)
	}
	if _, err := os.Stat(filepath.Join(workspace, "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("the command bypassed the read-only sandbox: %v", err)
	}
}

func TestCodexExecReadsEscapedMegabyteCommandResult(t *testing.T) {
	var final ChatCodeRun
	sink := codexEvents{commandRuns: make(map[string]*commandRun), emit: func(stream string, text string) error {
		if stream == chatCodeStream {
			return json.Unmarshal([]byte(text), &final)
		}
		return nil
	}}
	var wire bytes.Buffer
	encoder := json.NewEncoder(&wire)
	output := strings.Repeat("\x1b", (1<<20)-4) + "end\n"
	item := func(id, kind, status string) map[string]any {
		return map[string]any{"id": id, "type": kind, "status": status, "command": "fixture"}
	}
	if err := encoder.Encode(map[string]any{"type": "item.started", "item": item("item_1", "command_execution", "in_progress")}); err != nil {
		t.Fatal(err)
	}
	finished := item("item_1", "command_execution", "completed")
	finished["exit_code"], finished["aggregated_output"] = 0, output
	if err := encoder.Encode(map[string]any{"type": "item.completed", "item": finished}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "Result saved."}}); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.WriteTo(&sink); err != nil {
		t.Fatal(err)
	}
	if final.Status != Succeeded || final.ExitCode == nil || *final.ExitCode != 0 ||
		!final.Truncated || !strings.HasSuffix(final.Stdout, "end\n") || sink.answer != "Result saved." {
		t.Fatalf("status=%s exit=%v bytes=%d truncated=%v reply=%q", final.Status, final.ExitCode, len(final.Stdout), final.Truncated, sink.answer)
	}
}

func TestCodexExecutorKeepsOverlappingCommands(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	job, err := store.QueueVoiceHandoff(ctx, DefaultConversationID, "command-fixture", "run-tests", Handoff{
		AgentID: DefaultAgentID, Title: "Run tests", Brief: "Run both test commands.", ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); err != nil {
		t.Fatal(err)
	}
	var snapshots []ChatCodeRun
	progress := execCommandProgress{
		pending: make(map[string]execCommandStart),
		runs:    make(map[string]*execProcessRun),
		emit: func(stream, text string) error {
			var run ChatCodeRun
			if err := json.Unmarshal([]byte(text), &run); err != nil {
				return err
			}
			snapshots = append(snapshots, run)
			return store.append(ctx, job.ID, stream, text)
		},
	}
	for _, frame := range []struct {
		request bool
		json    string
	}{
		{true, `{"id":1,"method":"process/start","params":{"processId":"A","argv":["test-A"],"metadata":{"toolCallId":"one"}}}`},
		{false, `{"id":1,"result":{"processId":"A"}}`},
		{true, `{"id":2,"method":"process/start","params":{"processId":"B","argv":["test-B"],"metadata":{"toolCallId":"two"}}}`},
		{false, `{"id":2,"result":{"processId":"B"}}`},
		{false, `{"method":"process/output","params":{"processId":"A","stream":"stdout","chunk":"Zmlyc3QK"}}`},
		{false, `{"method":"process/output","params":{"processId":"B","stream":"stderr","chunk":"ZXJyb3IK"}}`},
		{false, `{"method":"process/exited","params":{"processId":"B","exitCode":1}}`},
		{false, `{"method":"process/output","params":{"processId":"A","stream":"stdout","chunk":"bGFzdAo="}}`},
		{false, `{"method":"process/exited","params":{"processId":"A","exitCode":0}}`},
	} {
		if frame.request {
			progress.request([]byte(frame.json))
		} else {
			progress.response([]byte(frame.json))
		}
	}
	if len(snapshots) != 6 {
		t.Fatalf("progress = %+v", snapshots)
	}
	first, second := snapshots[5], snapshots[4]
	if first.RunID == second.RunID || first.RunID != snapshots[0].RunID || second.RunID != snapshots[1].RunID ||
		first.Stdout != "first\nlast\n" || first.Status != Succeeded || second.Stderr != "error\n" || second.Status != Failed {
		t.Fatalf("finished commands: %+v and %+v", first, second)
	}
	details, err := store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || len(details.CodeRuns) != 2 {
		t.Fatalf("command history: %+v, %v", details.CodeRuns, err)
	}
	for _, want := range []ChatCodeRun{first, second} {
		run, err := store.ChatCodeOutput(ctx, DefaultConversationID, job.ID+"/"+want.RunID)
		if err != nil || run.Stdout != want.Stdout || run.Stderr != want.Stderr ||
			run.Status != want.Status || run.ExitCode == nil || *run.ExitCode != *want.ExitCode {
			t.Fatalf("stored command %s: %+v, %v", want.Command, run, err)
		}
	}
}
