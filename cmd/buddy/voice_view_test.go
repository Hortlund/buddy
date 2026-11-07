package main

import (
	"testing"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func TestCallRoomSelectsSpeakerAndKeepsCallOpenInChat(t *testing.T) {
	call := &desktopVoice{
		title: "Workshop", chat: "conv_workshop", ready: true, micReady: true, expanded: true, started: time.Now(),
		members: []api.VoiceMember{{ID: "buddy", Name: "Buddy"}, {ID: "scout", Name: "Scout"}},
		focus:   "buddy", lead: "buddy", speaking: "buddy", speaker: "Buddy", caption: "Hey, I’m here.",
		commands: make(chan api.VoiceEvent, 2),
	}
	job := tasks.WorkItem{ID: "build", ConversationID: call.chat, Title: "Check the build", Status: tasks.Running}
	call.work.items = []tasks.WorkItem{job}
	a := &desktop{voice: call, online: true, serverProtocol: api.VoiceStopWorkProtocol - 1}
	view := ui.NewTester(func(c *ui.Context) {
		if call.expanded {
			a.callView(c)
		} else {
			a.voicePanel(c)
		}
	}, 820, 590)
	view.SetTitleBar(ui.TitleBar{Height: 32, Left: 80})
	if title, ok := view.Find("Buddy"); !ok || title.X < 96 {
		t.Fatalf("call title crowds the window controls: %+v", title)
	}
	for _, label := range []string{"Buddy · Speaking · Lead Bud", "Invite Buds", "Hang up"} {
		if _, ok := view.Find(label); !ok {
			t.Fatalf("%s is missing from the call room", label)
		}
	}
	if err := view.Click("Work · 1"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("Open a job to see its progress or stop it.") {
		t.Fatal("the call did not offer the manual stop on an older server")
	}
	a.serverProtocol = api.VoiceStopWorkProtocol
	view.Frame()
	if !view.HasText("Say “stop that job” or open a job to see its progress and controls.") {
		t.Fatal("the call did not advertise spoken stop on a compatible server")
	}
	if err := view.Click("Open Check the build"); err != nil {
		t.Fatal(err)
	}
	call.work.detail = tasks.WorkDetails{WorkItem: job, Brief: "Verify the test suite.", Events: []tasks.Event{{Seq: 1, Text: "Tests started."}}}
	view.Frame()
	if !view.HasText("Tests started.") || !view.HasText("Verify the test suite.") || call.ending {
		t.Fatal("job progress was not visible alongside the live call")
	}
	if _, ok := view.Find("Stop job"); !ok {
		t.Fatal("the running job has no stop control in the call")
	}
	if err := view.Click("Work · 1"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Scout · In the call"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Mute"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []api.VoiceEvent{{Type: "focus", Agent: "scout"}, {Type: "mute", Muted: true}} {
		select {
		case event := <-call.commands:
			if event.Type != want.Type || event.Agent != want.Agent || event.Muted != want.Muted {
				t.Fatalf("call command = %+v, want %+v", event, want)
			}
		default:
			t.Fatalf("missing %s command", want.Type)
		}
	}
	call.micReady = false
	if err := view.Click("Unmute"); err != nil {
		t.Fatalf("Unmute during device recovery: %v", err)
	}
	select {
	case event := <-call.commands:
		if event.Type != "mute" || event.Muted {
			t.Fatalf("expected an unmute command, got %+v", event)
		}
	default:
		t.Fatal("Unmute did not send a command during device recovery")
	}
	if err := view.Click("Back to chat"); err != nil {
		t.Fatal(err)
	}
	if call.expanded || a.voice != call || call.ending || a.currentConversation() != call.chat {
		t.Fatal("returning to chat should keep the call connected")
	}
	if err := view.Click("Return to call"); err != nil || !call.expanded {
		t.Fatalf("reopening the call: %v", err)
	}
}

func TestWorkStopIgnoresDetailsReadBeforeCancellation(t *testing.T) {
	job := tasks.WorkItem{ID: "job", Status: tasks.NeedsInput, InputID: "question"}
	work := workBrowser{items: []tasks.WorkItem{job}}
	work.selectItem(job)
	stale := tasks.WorkDetails{WorkItem: job, Question: "Which folder?"}
	work.detail, work.reading, work.loading = stale, true, true
	poll := work.pollVersion
	work.noteStopped(tasks.Task{ID: job.ID, Status: tasks.Cancelled})
	work.finishDetailRead(work.scopeVersion, poll, work.selectionVersion, stale, nil)
	if work.detail.Status != tasks.Cancelled || work.selected.Status != tasks.Cancelled ||
		work.items[0].Status != tasks.Cancelled || work.detail.Question != "" || work.detail.InputID != "" ||
		work.reading || work.loading {
		t.Fatalf("the stop was overwritten or blocked the next refresh: %+v", work.detail)
	}
	final := tasks.WorkDetails{WorkItem: tasks.WorkItem{ID: job.ID, Status: tasks.Cancelled}, Result: "Stopped while waiting."}
	work.finishDetailRead(work.scopeVersion, work.pollVersion, work.selectionVersion, final, nil)
	if work.detail.Result != final.Result {
		t.Fatal("the fresh result did not replace the local stop status")
	}
}
