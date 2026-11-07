package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGroupAutoRoutesAndLetsAnotherBudJoinTheReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	forge, err := store.CreateAgent(ctx, Agent{Name: "Forge", Role: "Build engineer"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Team", Members: []string{DefaultAgentID, forge.ID}})
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(store, nil, func(_ context.Context, turn Turn, _ Emit) (string, error) {
		if turn.Origin == "peer_reply" {
			if turn.Agent.ID != DefaultAgentID || !strings.Contains(turn.Brief, "test") || turn.Agent.AllowEdits || turn.coordinate ||
				!slices.ContainsFunc(turn.Messages, func(m Message) bool { return m.Text == "How do we fix the build?" }) {
				return "", errors.New("wrong peer context")
			}
			// Even a misbehaving runner cannot create a second peer turn.
			turn.action.Peer = &PeerQuestion{AgentID: forge.ID, Question: "Try handing back"}
			return "I can check the test strategy.", nil
		}
		if turn.Agent.ID == forge.ID {
			if turn.Agent.Workspace != forge.Workspace || turn.Agent.Role != forge.Role {
				return "", errors.New("wrong routed workspace")
			}
			turn.action.Peer = &PeerQuestion{AgentID: DefaultAgentID, Question: "What would you test?"}
			return "I can check the build.", nil
		}
		return "Lead replied.", nil
	})
	routed := make(chan GroupRouting, 2)
	worker.ChooseGroupBud = func(_ context.Context, input GroupRouting) string {
		routed <- input
		return forge.ID
	}
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	defer func() { cancel(); <-done }()
	parent, _, err := store.Send(ctx, group.ID, "How do we fix the build?", "auto")
	if err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	waitForConversation(t, store, group.ID, func(c Conversation) bool {
		return slices.ContainsFunc(c.Messages, func(m Message) bool {
			return m.TaskID == parent.ID && m.AgentID == forge.ID && m.Role == "assistant"
		}) && slices.ContainsFunc(c.Messages, func(m Message) bool {
			return m.AgentID == DefaultAgentID && strings.Contains(m.Text, "check the test strategy")
		})
	})
	input := <-routed
	if input.Lead != DefaultAgentID || input.Message != "How do we fix the build?" || len(input.Buds) != 2 {
		t.Fatalf("unexpected routing context: %+v", input)
	}
	if retry, created, err := store.Send(ctx, group.ID, "How do we fix the build?", "auto"); err != nil || created || retry.ID != parent.ID {
		t.Fatalf("routing broke a retry: %+v %v", retry, err)
	}
	explicit, _, err := store.Send(ctx, group.ID, "Buddy, hello.", "named")
	if err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	waitForConversation(t, store, group.ID, func(c Conversation) bool {
		return slices.ContainsFunc(c.Messages, func(m Message) bool { return m.TaskID == explicit.ID && m.Text == "Lead replied." })
	})
	select {
	case <-routed:
		t.Fatal("a clearly addressed Bud did not bypass the model router")
	default:
	}
}

func TestArchiveGroupAndCallPreservesWorkAndRestoresTranscript(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	buddy, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	forge, err := store.CreateAgent(ctx, Agent{Name: "Forge"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Team", Members: []string{DefaultAgentID, forge.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordVoiceLine(ctx, group.ID, "one", buddy, "user", "Only in this call."); err != nil {
		t.Fatal(err)
	}
	job, err := store.QueueVoiceWork(ctx, group.ID, DefaultAgentID, "one", "job", "Read a report", "Read it.")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCallArchived(ctx, DefaultConversationID, "one", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("call changed from the wrong conversation: %v", err)
	}
	if err := store.SetCallArchived(ctx, group.ID, "one", true); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, group.ID, 0)
	if err != nil || len(chat.Messages) != 0 || len(chat.Work) != 1 || chat.Work[0].ID != job.ID || chat.TranscriptRevision == 0 {
		t.Fatalf("archive affected the wrong data: %+v, %v", chat, err)
	}
	if err := store.SetCallArchived(ctx, group.ID, "one", false); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Conversation(ctx, group.ID, 0)
	if err != nil || len(restored.Messages) != 1 || restored.TranscriptRevision <= chat.TranscriptRevision {
		t.Fatalf("call restore did not update the transcript: %+v, %v", restored, err)
	}
	if _, err := store.AddRoutine(ctx, group.ID, Routine{Name: "Daily", Prompt: "Do work.", Minutes: 1440}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupArchived(ctx, group.ID, true); err != nil {
		t.Fatal(err)
	}
	directory, err := store.Directory(ctx)
	if err != nil || len(directory.Groups) != 0 || len(directory.ArchivedGroups) != 1 {
		t.Fatalf("archive did not move the group in the directory: %+v, %v", directory, err)
	}
	if _, _, err := store.Send(ctx, group.ID, "Do more.", "archived"); err == nil {
		t.Fatal("an archived group accepted a new message")
	}
	chat, err = store.Conversation(ctx, group.ID, 0)
	if err != nil || !chat.Archived || len(chat.Routines) != 1 || chat.Routines[0].Enabled || !chat.Work[0].ChatArchived {
		t.Fatalf("archived group state: %+v, %v", chat, err)
	}
}

func TestBudLearnsFromPrivateTurnsAndKeepsSoulEditsVersioned(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := store.CreateAgent(ctx, Agent{Name: "Forge"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Team", Members: []string{DefaultAgentID, other.ID}})
	if err != nil {
		t.Fatal(err)
	}
	learn := func(chat, message, note string) {
		t.Helper()
		job, _, err := store.Send(ctx, chat, message, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.claim(ctx); err != nil {
			t.Fatal(err)
		}
		action := &TurnAction{Learning: []LearnedNote{{Topic: "reply-length", Note: note, Evidence: message}}}
		if err := store.finishTurn(ctx, job.ID, Succeeded, "Replied.", "Got it.", action); err != nil {
			t.Fatal(err)
		}
	}
	learn(DefaultConversationID, "I prefer short replies.", "Prefers short replies.")
	learn(DefaultConversationID, "I prefer detailed replies now.", "Prefers detailed replies.")
	learn(group.ID, "I prefer verbose reports.", "Prefers verbose reports.")
	initial, err := store.BudDocuments(ctx, DefaultAgentID)
	if err != nil || !strings.Contains(initial.Memory, "detailed replies") ||
		strings.Contains(initial.Memory, "short replies") || strings.Contains(initial.Memory, "verbose") {
		t.Fatalf("wrong learned notes: %+v, %v", initial, err)
	}
	if err := store.SaveSoul(ctx, DefaultAgentID, "# Soul\n\nBe direct.", initial.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSoul(ctx, DefaultAgentID, "Stale edit", initial.Revision); err == nil {
		t.Fatal("stale editor overwrote SOUL.md")
	}
	if err := store.SetBudLearning(ctx, DefaultAgentID, false); err != nil {
		t.Fatal(err)
	}
	learn(DefaultConversationID, "I prefer extra detail now.", "Prefers long replies.")
	job, _, err := store.Send(ctx, group.ID, "Hi group", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, job)
	if err != nil || turn.LearnedMemory != "" || turn.CanLearn || !strings.Contains(turn.Soul, "Be direct.") {
		t.Fatalf("group context included private memory or missed the soul: %+v, %v", turn, err)
	}
	final, err := store.BudDocuments(ctx, DefaultAgentID)
	if err != nil || final.Learning || final.Memory != initial.Memory {
		t.Fatalf("paused learning changed memory: %+v, %v", final, err)
	}
}
