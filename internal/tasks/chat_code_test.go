package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChatCodeRunsInlineAndKeepsHostFilesPrivate(t *testing.T) {
	sandbox, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is unavailable")
	}
	runtimes := chatCodeRuntimes()
	if _, ok := runtimes["go"]; !ok {
		t.Skip("Go is unavailable")
	}

	const message = "Take the example above and run it for me."
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, _, err := store.Send(ctx, DefaultConversationID, message, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(private, []byte("PRIVATE DATA"), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package main
import ("fmt";"os")
func main() {
  _, err := os.ReadFile(` + tomlString(private) + `)
  fmt.Printf("Read blocked: %t\n", err != nil)
  a,b := 0,1
  for i:=0;i<12;i++ { fmt.Printf("%d ",a); a,b=b,a+b }
}
`
	action := TurnAction{}
	turn := Turn{coordinate: true, Origin: "chat", action: &action, codeSandbox: sandbox, codeRuntimes: runtimes,
		Messages: []Message{{Role: "user", Text: message}}}
	var progress []string
	calls := 0
	reply, err := turn.complete(ctx, "", "prompt", nil, func(stream, text string) error {
		progress = append(progress, text)
		if err := store.append(ctx, task.ID, stream, text); err != nil {
			return err
		}
		if stream == chatCodeStream && len(progress) == 2 {
			chat, err := store.Conversation(ctx, DefaultConversationID, 0)
			if err != nil || chat.Active == nil || chat.Active.CodeRun == nil ||
				chat.Active.CodeRun.Status != Running || chat.Active.CodeRun.Command != "go build -o program main.go && ./program" {
				t.Fatalf("chat did not expose the live run: %+v, %v", chat.Active, err)
			}
		}
		return nil
	}, func(prompt string) (string, error) {
		calls++
		if calls == 1 {
			data, _ := json.Marshal(struct {
				Reply string          `json:"reply"`
				Run   chatCodeRequest `json:"run_code"`
			}{"", chatCodeRequest{"go", source}})
			return string(data), nil
		}
		t.Fatal("program output was sent back to the model")
		return "", nil
	})
	if err != nil || calls != 1 || !strings.Contains(reply, "Exit status: 0") || len(progress) < 2 || action.RunCode != nil || action.hasHandoff() {
		t.Fatalf("didn't finish the inline run: %q, %v, %v, %v", reply, err, progress, action)
	}
	if err := store.finish(ctx, task.ID, Succeeded, "Finished", reply); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || chat.Active != nil || len(chat.Messages) != 2 || chat.Messages[0].CodeRun != nil {
		t.Fatalf("unexpected transcript after completing run: %+v, %v", chat, err)
	}
	run := chat.Messages[1].CodeRun
	if run == nil || run.Status != Succeeded || run.ExitCode == nil || *run.ExitCode != 0 ||
		!strings.Contains(run.Stdout, "Read blocked: true") ||
		!strings.HasSuffix(strings.TrimSpace(run.Stdout), "0 1 1 2 3 5 8 13 21 34 55 89") || run.FinishedAt == nil {
		t.Fatalf("chat did not preserve the completed run: %+v", run)
	}
	turn.chatTools = &chatToolState{}
	_, tools := turn.toolsForChat(ctx, "", nil, func(string, string) error { return nil })
	args, err := json.Marshal(chatCodeRequest{Language: "go", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	value, err := tools["run_code"].run(args)
	result, ok := value.(chatToolText)
	var recorded chatCodeResult
	if err != nil || !ok || !result.success || json.Unmarshal([]byte(result.text), &recorded) != nil || recorded.Stdout != run.Stdout {
		t.Fatalf("the model did not receive the measured output: %+v, %v", result, err)
	}
}

func TestChatCodeRequiresAnActiveChatRequest(t *testing.T) {
	turn := Turn{coordinate: true, Origin: "chat", BuddyTeam: TeamContext{CurrentTaskID: "current"},
		codeRuntimes: map[string]chatCodeRuntime{"python": {}},
		Messages:     []Message{{Role: "user", TaskID: "current", Text: "Could you check whether the example works?"}}}
	if !turn.canRunChatCode() {
		t.Fatal("the current chat request should have access to the runner")
	}
	turn.Messages = append(turn.Messages, Message{Role: "user", TaskID: "other", Text: "Run it"})
	if turn.canRunChatCode() {
		t.Fatal("a different chat turn should not have access to the runner")
	}
}

func TestChatCodePreviewLeavesFullOutputAvailable(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, _, err := store.Send(ctx, DefaultConversationID, "Run it", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	log := strings.Repeat("ö▉\x00", 400) + " end"
	run := ChatCodeRun{Language: "python", Status: Running, StartedAt: time.Now(), chatCodeResult: chatCodeResult{Stdout: log}}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.append(ctx, task.ID, chatCodeStream, string(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.cancelScoped(ctx, task.ID, false, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop from other chat = %v", err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || chat.Active == nil || chat.Active.CodeRun == nil || !chat.Active.CodeRun.OutputPreview ||
		len(chat.Active.CodeRun.Stdout) > 512 || !strings.HasSuffix(log, chat.Active.CodeRun.Stdout) {
		t.Fatalf("wrong active preview: %+v, %v", chat.Active, err)
	}
	if _, err := store.cancelScoped(ctx, task.ID, false, DefaultConversationID); err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, task.ID, Cancelled, "Stopped", "Stopped"); err != nil {
		t.Fatal(err)
	}
	read, err := store.ChatCodeOutput(ctx, DefaultConversationID, task.ID)
	if err != nil || read.Stdout != log || read.OutputPreview || read.Status != Cancelled || read.FinishedAt == nil {
		t.Fatalf("full output missing, %v", err)
	}
	if stopped, err := store.cancelScoped(ctx, task.ID, false, DefaultConversationID); err != nil || stopped.Status != Cancelled {
		t.Fatalf("repeated stop should succeed, got %+v, %v", stopped, err)
	}
	if _, err := store.ChatCodeOutput(ctx, "other", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("output from other chat = %v", err)
	}
	chat, err = store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 2 || chat.Messages[1].CodeRun == nil || !chat.Messages[1].CodeRun.OutputPreview {
		t.Fatalf("wrong completed preview: %+v, %v", chat.Messages, err)
	}
}

func TestChatCodeOutputKeepsTailAcrossWrites(t *testing.T) {
	var stream codeOutput
	output := ""
	for _, chunk := range []string{
		strings.Repeat("x", maxChatCodeOutput-2), "ö界", "progress\n",
		strings.Repeat("a", maxChatCodeOutput+17), "ñ", "final summary\n",
	} {
		output += chunk
		_, _ = stream.Write([]byte(chunk))
		want := output[max(0, len(output)-maxChatCodeOutput):]
		for !utf8.RuneStart(want[0]) {
			want = want[1:]
		}
		if got := stream.String(); got != want {
			t.Fatalf("captured %d bytes, want the latest %d bytes", len(got), len(want))
		}
	}
	if !stream.truncated || !strings.HasSuffix(stream.String(), "final summary\n") {
		t.Fatal("stream lost its final line or truncation state")
	}
}

func TestChatCodeUpdatesOneStoredSnapshot(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, _, err := store.Send(ctx, DefaultConversationID, "Run it", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP INDEX events_code_run;
		INSERT INTO events(task_id,at,stream,text) VALUES
			(?,0,'chat_code_run','old'),(?,1,'chat_code_run','new')`, task.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	var content string
	check := func(want string) {
		t.Helper()
		if err := store.db.QueryRowContext(ctx, "SELECT count(*),max(text) FROM events WHERE task_id=? AND stream=?", task.ID, chatCodeStream).
			Scan(&count, &content); err != nil || count != 1 || content != want {
			t.Fatalf("stored %d snapshots, latest %q, want %q, error %v", count, content, want, err)
		}
	}
	check("new")
	for _, content := range []string{`{"stdout":"first"}`, `{"stdout":"last"}`} {
		if err := store.append(ctx, task.ID, chatCodeStream, content); err != nil {
			t.Fatal(err)
		}
	}
	check(`{"stdout":"last"}`)
}

func TestInlineRunsKeepSeparateOutput(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, _, err := store.Send(ctx, DefaultConversationID, "Run both snippets.", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	emit := func(stream, data string) error { return store.append(ctx, task.ID, stream, data) }
	for i, id := range []string{"run_first", "run_second"} {
		run := ChatCodeRun{RunID: id, Status: Running, StartedAt: time.Now().UTC()}
		if err := publishCodeRun(emit, run); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			current, err := store.cancelTaskInScope(ctx, task.ID, false, DefaultConversationID, false, "run_first")
			if err != nil || current.Status != Running {
				t.Fatalf("a stale cancellation changed the live run: %+v, %v", current, err)
			}
		}
		code := i
		done := time.Now().UTC()
		run.Status, run.FinishedAt, run.ExitCode, run.Stdout = Succeeded, &done, &code, id+" output"
		if err := publishCodeRun(emit, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.finish(ctx, task.ID, Succeeded, "Finished", "Ran both."); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 2 || len(chat.Messages[1].CodeRuns) != 2 {
		t.Fatalf("missing runs in transcript: %+v, %v", chat.Messages, err)
	}
	for i, id := range []string{"run_first", "run_second"} {
		run, err := store.ChatCodeOutput(ctx, DefaultConversationID, task.ID+"/"+id)
		if err != nil || run.RunID != id || run.Stdout != id+" output" || run.ExitCode == nil || *run.ExitCode != i {
			t.Fatalf("wrong output for %s: %+v, %v", id, run, err)
		}
	}
	if _, err := store.ChatCodeOutput(ctx, "other_chat", task.ID+"/run_first"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexpected cross-chat result: %v", err)
	}
}

func TestChatCodeResultUsesSeparateReadOnlyExplanation(t *testing.T) {
	run := ChatCodeRun{Status: Failed, chatCodeResult: chatCodeResult{Stderr: "actual fixture error"}}
	action := TurnAction{}
	turn := Turn{coordinate: true, Origin: "chat", action: &action,
		Messages: []Message{{Role: "assistant", TaskID: "this-chat", CodeRun: &run}}}
	turn.codeResult = func(_ context.Context, id string) (ChatCodeRun, error) {
		if id != "this-chat" {
			t.Fatal("accessed output from another chat")
		}
		return run, nil
	}
	turn.explainCodeResult = func(_ context.Context, measured ChatCodeRun) (string, error) {
		if measured.Stderr != "actual fixture error" {
			t.Fatalf("wrong recorded result: %+v", measured)
		}
		return "Explained the recorded error.", nil
	}
	for _, id := range []string{"this-chat", "different-chat"} {
		var modelCalls int
		reply, err := turn.complete(t.Context(), "", "", nil, nil, func(prompt string) (string, error) {
			modelCalls++
			if strings.Contains(prompt, run.Stderr) {
				t.Fatal("raw result leaked into the tool-enabled session")
			}
			return `{"reply":"","read_code_result":"` + id + `","handoffs":[{"title":"unrelated"}]}`, nil
		})
		if err != nil || modelCalls != 1 || (id == "this-chat" && reply != "Explained the recorded error.") ||
			(id == "different-chat" && !strings.Contains(reply, "couldn’t find")) || action.hasHandoff() {
			t.Fatalf("result lookup %q returned %q, %v, %+v", id, reply, err, action)
		}
	}
}

func TestInlinePythonCannotWriteFiles(t *testing.T) {
	sandbox, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is unavailable")
	}
	python, ok := chatCodeRuntimes()["python"]
	if !ok {
		t.Skip("Python is unavailable")
	}
	if chatCodeSandbox(sandbox) == "" {
		t.Skip("A native sandbox executable is unavailable")
	}
	result := runChatCode(t.Context(), sandbox, python, `import os, resource
assert resource.getrlimit(resource.RLIMIT_FSIZE) == (0, 0)
for path in ("new-file", "tmp/new-file", "/tmp/buddy-write-fixture", "/var/tmp/buddy-write-fixture", "/dev/shm/buddy-write-fixture"):
    try:
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except OSError:
        print("blocked")
    else:
        os.close(fd)
        os.unlink(path)
        raise RuntimeError("created " + path)
`, nil)
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != strings.Repeat("blocked\n", 5) {
		t.Fatalf("filesystem was not bounded: %+v", result)
	}
}

func TestInlinePythonStopsAtMemoryBudget(t *testing.T) {
	sandbox, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is unavailable")
	}
	python, ok := chatCodeRuntimes()["python"]
	if !ok || chatCodeSandbox(sandbox) == "" {
		t.Skip("Sandboxed Python is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := runChatCode(ctx, sandbox, python, `import time
blocks = []
for _ in range(40):
    blocks.append(bytearray(b"x") * (8 << 20))
    time.sleep(0.015)
print("over budget")
time.sleep(3)
`, nil)
	if ctx.Err() != nil || result.Error != "The run exceeded its memory limit." ||
		strings.Contains(result.Stdout, "over budget") {
		t.Fatalf("memory budget did not stop the run: %+v", result)
	}
}

func TestChatCodeRejectsShortMemorySpike(t *testing.T) {
	python, ok := chatCodeRuntimes()["python"]
	if !ok {
		t.Skip("A bounded Python runner is unavailable")
	}
	sandbox, _ := exec.LookPath("codex")
	result := runChatCode(t.Context(), sandbox, python, `block = bytearray(300 << 20)
del block
print("over budget")
`, nil)
	if result.Error != "The run exceeded its memory limit." || strings.Contains(result.Stdout, "over budget") {
		t.Fatalf("memory spike was not stopped: %+v", result)
	}
	if python.image != "" && (result.ExitCode == nil || *result.ExitCode != 137) {
		t.Fatalf("container did not report the exit code for its memory kill: %+v", result)
	}
}

func TestChatCodeContainerBuildsStandardLibrarySnippet(t *testing.T) {
	goRuntime, ok := containerChatCodeRuntimes()["go"]
	if !ok {
		t.Skip("The pinned Go container is unavailable")
	}
	result := runChatCode(t.Context(), "", goRuntime, `package main
import ("fmt"; "net/http"; "os")
func main() {
    if os.Geteuid() == 0 { panic("snippet ran as root") }
    if _, err := os.Create("new-file"); err == nil { panic("snippet could write to scratch") }
    fmt.Println(http.MethodGet)
}
`, nil)
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "GET\n" {
		t.Fatalf("standard library snippet failed: %+v", result)
	}
}

func TestChatCodeCapsProcesses(t *testing.T) {
	sandbox, _ := exec.LookPath("codex")
	python, ok := chatCodeRuntimes()["python"]
	if !ok {
		t.Skip("Sandboxed Python unavailable")
	}
	result := runChatCode(t.Context(), sandbox, python, `import os, time
children = []
try:
    for _ in range(70):
        try:
            child = os.fork()
        except BlockingIOError:
            print("limited")
            break
        if child == 0:
            time.sleep(1)
            os._exit(0)
        children.append(child)
finally:
    for child in children:
        os.waitpid(child, 0)
`, nil)
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "limited\n" {
		t.Fatalf("process budget was not enforced: %+v", result)
	}
}

func TestInlinePythonStreamsOutputAndReportsFailure(t *testing.T) {
	sandbox, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is unavailable")
	}
	runtime, ok := chatCodeRuntimes()["python"]
	if !ok {
		t.Skip("Python is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var streamed bool
	result := runChatCode(ctx, sandbox, runtime, "import time, sys\nprint(42)\ntime.sleep(1.2)\nprint('example error', file=sys.stderr)\nsys.exit(3)", func(output chatCodeResult) {
		streamed = streamed || output.Stdout == "42\n" && output.Stderr == ""
	})
	if !streamed || result.ExitCode == nil || *result.ExitCode != 3 ||
		result.Stdout != "42\n" || result.Stderr != "example error\n" {
		t.Fatalf("inline Python streamed=%t, result=%+v", streamed, result)
	}
}

func TestInlinePythonCannotLeaveDetachedChild(t *testing.T) {
	sandbox, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is unavailable")
	}
	python, ok := chatCodeRuntimes()["python"]
	if !ok {
		t.Skip("Python is unavailable")
	}
	if runtime.GOOS == "linux" || python.image != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		result := runChatCode(ctx, sandbox, python, `import os, time
reader, writer = os.pipe()
if os.fork() == 0:
    os.setsid()
    print("detached", flush=True)
    os.write(writer, b"1")
    time.sleep(20)
    print("escaped", flush=True)
    os._exit(0)
os.close(writer)
assert os.read(reader, 1) == b"1"
print("finished", flush=True)
`, nil)
		if ctx.Err() != nil || result.ExitCode == nil || *result.ExitCode != 0 ||
			result.Stdout != "detached\nfinished\n" {
			t.Fatalf("namespace failed to clean up descendants on exit: %+v, %v", result, ctx.Err())
		}
		return
	}
	result := runChatCode(t.Context(), sandbox, python, `import os, resource, subprocess
assert resource.getrlimit(resource.RLIMIT_NPROC) == (0, 0)
for spawn in (os.fork, lambda: os.posix_spawn("/usr/bin/true", ["true"], {}), lambda: subprocess.run(["/usr/bin/true"])):
    try:
        spawned = spawn()
    except OSError:
        print("blocked")
    else:
        if spawned == 0:
            os._exit(0)
        raise RuntimeError("started child")
`, nil)
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "blocked\nblocked\nblocked\n" {
		t.Fatalf("process isolation failed: %+v", result)
	}
}

func TestInlinePythonStopsOnCancellation(t *testing.T) {
	sandbox, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is unavailable")
	}
	python, ok := chatCodeRuntimes()["python"]
	if !ok {
		t.Skip("Python is unavailable")
	}
	deadline, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	var began bool
	result := runChatCode(ctx, sandbox, python, `import signal, time
signal.signal(signal.SIGINT, signal.SIG_IGN)
print("started")
time.sleep(25)
print("kept running")
`, func(output chatCodeResult) {
		if output.Stdout == "started\n" {
			began = true
			cancel()
		}
	})
	if !began || deadline.Err() != nil || result.ExitCode != nil || result.Stdout != "started\n" {
		t.Fatalf("inline runner did not stop on cancellation: %+v", result)
	}
}
