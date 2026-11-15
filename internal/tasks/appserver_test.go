package tasks

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

func TestAppServerExecutesChatToolOnlyForCurrentTurn(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	calls := 0
	session := appSession{input: write, threadID: "chat", turn: appTurn{ID: "current", Status: "inProgress"},
		chatTools: map[string]chatTool{
			"run_code": defineChatTool("run_code", "", chatCodeSchema, func(request chatCodeRequest) (any, error) {
				calls++
				if request.Language != "python" || request.Source != "print(42)" {
					t.Fatalf("unexpected tool arguments: %+v", request)
				}
				return chatToolText{text: "Exit status: 0", success: true}, nil
			}),
		}}
	decoder := json.NewDecoder(read)
	for _, tc := range []struct {
		turnID string
		want   bool
	}{{"earlier", false}, {"current", true}, {"current", true}} {
		args, err := json.Marshal(map[string]any{
			"threadId": "chat", "turnId": tc.turnID, "callId": "same-call", "tool": "run_code",
			"arguments": chatCodeRequest{Language: "python", Source: "print(42)"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := session.handle(appFrame{Method: "item/tool/call", ID: json.RawMessage(`"call-1"`), Params: args}); err != nil {
			t.Fatal(err)
		}
		var response struct {
			ID     string `json:"id"`
			Result struct {
				Success bool `json:"success"`
			} `json:"result"`
		}
		if err := decoder.Decode(&response); err != nil || response.ID != "call-1" || response.Result.Success != tc.want {
			t.Fatalf("unexpected tool response for %s: %+v, %v", tc.turnID, response, err)
		}
	}
	if calls != 1 {
		t.Fatalf("tool invoked %d times, want 1", calls)
	}
}

func TestAppServerStreamsProjectCommandAndRecordsExit(t *testing.T) {
	var snapshots []ChatCodeRun
	session := appSession{threadID: "chat", turn: appTurn{ID: "current", Status: "inProgress"},
		events: codexEvents{commandRuns: make(map[string]*commandRun), emit: func(stream, payload string) error {
			if stream == chatCodeStream {
				var command ChatCodeRun
				if err := json.Unmarshal([]byte(payload), &command); err != nil {
					return err
				}
				snapshots = append(snapshots, command)
			}
			return nil
		}}}
	for _, frame := range []appFrame{
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"chat","turnId":"current","item":{"type":"commandExecution","id":"command","command":"go test ./...","status":"inProgress"}}`)},
		{Method: "item/commandExecution/outputDelta", Params: json.RawMessage(`{"threadId":"chat","turnId":"earlier","itemId":"command","delta":"old output"}`)},
		{Method: "item/commandExecution/outputDelta", Params: json.RawMessage(`{"threadId":"chat","turnId":"current","itemId":"command","delta":"FAIL\n"}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"threadId":"chat","turnId":"current","item":{"type":"commandExecution","id":"command","status":"failed","exitCode":1,"aggregatedOutput":"FAIL\n"}}`)},
	} {
		if err := session.handle(frame); err != nil {
			t.Fatal(err)
		}
	}
	if len(snapshots) != 3 || snapshots[0].Status != Running || snapshots[0].Command != "go test ./..." ||
		snapshots[1].Stdout != "FAIL\n" || snapshots[2].Status != Failed || snapshots[2].ExitCode == nil ||
		*snapshots[2].ExitCode != 1 || snapshots[2].Stdout != "FAIL\n" || !snapshots[2].WorkCommand {
		t.Fatalf("unexpected command snapshots: %+v", snapshots)
	}
}

func TestAppServerKeepsSeparateCommandResults(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	job, err := store.QueueVoiceWork(ctx, DefaultConversationID, DefaultAgentID, "commands", "run", "Test", "Run two tests.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); err != nil {
		t.Fatal(err)
	}
	session := appSession{threadID: "chat", turn: appTurn{ID: "current", Status: "inProgress"},
		events: codexEvents{commandRuns: make(map[string]*commandRun), emit: func(stream, payload string) error {
			return store.append(ctx, job.ID, stream, payload)
		}}}
	for _, frame := range []appFrame{
		{Method: "item/started", Params: json.RawMessage(`{"item":{"type":"commandExecution","id":"a","command":"test a"}}`)},
		{Method: "item/started", Params: json.RawMessage(`{"item":{"type":"commandExecution","id":"b","command":"test b"}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"item":{"type":"commandExecution","id":"b","status":"failed","exitCode":1,"aggregatedOutput":"failure"}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"item":{"type":"commandExecution","id":"a","status":"completed","exitCode":0,"aggregatedOutput":"success"}}`)},
	} {
		if err := session.handle(frame); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || len(detail.CodeRuns) != 2 {
		t.Fatalf("command results: %+v, %v", detail.CodeRuns, err)
	}
	first, second := detail.CodeRuns[0], detail.CodeRuns[1]
	if first.RunID == "" || second.RunID == "" || first.RunID == second.RunID ||
		first.Status != Succeeded || first.Stdout != "success" || second.Status != Failed || second.Stdout != "failure" {
		t.Fatalf("wrong results: %+v", detail.CodeRuns)
	}
}

func TestAppServerKeepsAnswerPartsAcrossSteering(t *testing.T) {
	session := appSession{threadID: "thread", events: codexEvents{emit: func(string, string) error { return nil }}}
	for _, params := range []string{
		`{"threadId":"thread","item":{"type":"agentMessage","phase":"commentary","text":"I will check."}}`,
		`{"threadId":"thread","item":{"type":"agentMessage","phase":"partial_answer","text":"Here is the original result."}}`,
		`{"threadId":"thread","item":{"type":"agentMessage","phase":"final_answer","text":"Here is the addendum."}}`,
		`{"threadId":"thread","item":{"type":"agentMessage","phase":"commentary","text":"Finishing up."}}`,
	} {
		if err := session.handle(appFrame{Method: "item/completed", Params: json.RawMessage(params)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := session.events.answer; got != "Here is the original result.\n\nHere is the addendum." {
		t.Fatalf("stored answer = %q", got)
	}
}

func TestAppServerStreamsPublicTextBeforeCompletion(t *testing.T) {
	var updates []string
	session := appSession{threadID: "thread", events: codexEvents{emit: func(stream, text string) error {
		if stream == "commentary" {
			updates = append(updates, text)
		}
		return nil
	}}}
	push := func(method, params string) {
		t.Helper()
		if err := session.handle(appFrame{Method: method, Params: json.RawMessage(params)}); err != nil {
			t.Fatal(err)
		}
	}
	push("item/started", `{"threadId":"thread","item":{"type":"agentMessage","id":"note","phase":"commentary"}}`)
	push("item/reasoning/textDelta", `{"threadId":"thread","itemId":"note","delta":"private"}`)
	push("item/agentMessage/delta", `{"threadId":"other","itemId":"note","delta":"other thread"}`)
	push("item/agentMessage/delta", `{"threadId":"thread","itemId":"other","delta":"other item"}`)
	push("item/agentMessage/delta", `{"threadId":"thread","itemId":"note","delta":"I’ll check"}`)
	session.messageSent = time.Now() // Keep this delta inside the write interval.
	push("item/agentMessage/delta", `{"threadId":"thread","itemId":"note","delta":" the project"}`)
	if len(updates) != 1 || updates[0] != "I’ll check" || session.events.answer != "" {
		t.Fatalf("live updates = %q, answer = %q", updates, session.events.answer)
	}
	session.messageSent = time.Time{}
	push("item/agentMessage/delta", `{"threadId":"thread","itemId":"note","delta":" tests."}`)
	push("item/completed", `{"threadId":"thread","item":{"type":"agentMessage","id":"note","phase":"commentary","text":"I’ll check the project tests."}}`)
	if len(updates) != 2 || updates[1] != "I’ll check the project tests." || session.events.answer != "" {
		t.Fatalf("completed updates = %q, answer = %q", updates, session.events.answer)
	}
}

func TestAppServerKeepsOnlyCompletedBackgroundUpdates(t *testing.T) {
	var notes []string
	session := appSession{threadID: "thread", background: true, events: codexEvents{emit: func(stream, text string) error {
		if stream == "work_update" || stream == "commentary" {
			notes = append(notes, text)
		}
		return nil
	}}}
	for _, frame := range []appFrame{
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"agentMessage","id":"note","phase":"commentary"}}`)},
		{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"threadId":"thread","itemId":"note","delta":"I checked"}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"agentMessage","id":"note","phase":"commentary","text":"I checked the workspace."}}`)},
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"agentMessage","id":"answer","phase":"final_answer"}}`)},
		{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"threadId":"thread","itemId":"answer","delta":"Could you"}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"agentMessage","id":"answer","phase":"final_answer","text":"Could you share the repository path?"}}`)},
	} {
		if err := session.handle(frame); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(notes, []string{"I checked the workspace."}) || session.events.answer != "Could you share the repository path?" {
		t.Fatalf("background updates = %q, reply = %q", notes, session.events.answer)
	}
}

func TestStructuredBackgroundReplyKeepsPriorWorkWhenAskingForInput(t *testing.T) {
	session := appSession{threadID: "thread", background: true, outputSchema: backgroundReplySchema,
		events: codexEvents{structured: true, emit: func(string, string) error { return nil }}}
	for _, answer := range []string{
		`{"reply":"I checked the build.","needs_input":false}`,
		`{"reply":"Which branch should I inspect next?","needs_input":true}`,
	} {
		payload, err := json.Marshal(map[string]any{
			"threadId": "thread", "item": map[string]string{"type": "agentMessage", "phase": "final_answer", "text": answer},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := session.handle(appFrame{Method: "item/completed", Params: payload}); err != nil {
			t.Fatal(err)
		}
	}
	reply, err := (Turn{CanRequestInput: true}).decodeReply(session.events.answer, nil)
	if err != ErrWorkNeedsInput || reply != "I checked the build.\n\nWhich branch should I inspect next?" {
		t.Fatalf("background reply = %q, %v", reply, err)
	}
}

func TestAppServerReportsToolKindAndPublicUpdates(t *testing.T) {
	var got []string
	session := appSession{threadID: "thread", events: codexEvents{emit: func(stream, text string) error {
		got = append(got, stream+": "+text)
		return nil
	}}}
	for _, frame := range []appFrame{
		{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"reasoning","text":"private"}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"agentMessage","phase":"commentary","text":"I’ll inspect the tests."}}`)},
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"commandExecution","commandActions":[{"type":"read"}]}}`)},
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread","item":{"type":"fileChange"}}`)},
	} {
		if err := session.handle(frame); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 3 || got[0] != "commentary: I’ll inspect the tests." ||
		got[1] != "activity: "+activityLabels[Reading] || got[2] != "activity: "+activityLabels[Editing] {
		t.Fatalf("visible updates = %v", got)
	}
}

func TestDeviceEnvironmentKeepsOnlyRuntimeVariables(t *testing.T) {
	input := []string{
		"PATH=/usr/bin", "HOME=/home/worker", "LC_ALL=C",
		"GITHUB_TOKEN=fixture", "DATABASE_URL=fixture", "BUDDY_CANARY=fixture",
		"CODEX_HOME=/old/config", "LD_PRELOAD=/unexpected/lib", "LC_UNEXPECTED=fixture",
	}
	want := []string{"PATH=/usr/bin", "HOME=/home/worker", "LC_ALL=C"}
	if got := deviceEnvironment(input); !slices.Equal(got, want) {
		t.Fatalf("worker environment = %v, want %v", got, want)
	}
}
