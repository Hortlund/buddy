package tasks

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

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
