package tasks

import (
	"path/filepath"
	"testing"
)

func TestVoiceGroupMovesOnlyCurrentCallAndKeepsAddingMembers(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "voice.db"))
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
	scout, err := store.CreateAgent(ctx, Agent{Name: "Scout"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPrimaryAgent(ctx, forge.ID); err != nil {
		t.Fatal(err)
	}
	const call = "current-call"
	if err := store.RecordVoiceLine(ctx, DefaultConversationID, "earlier-call", buddy, "user", "Private call."); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordVoiceLine(ctx, DefaultConversationID, call, buddy, "user", "Current call."); err != nil {
		t.Fatal(err)
	}
	work, err := store.QueueVoiceWork(ctx, DefaultConversationID, buddy.ID, call, "first", "Draft report", "Write a report")
	if err != nil {
		t.Fatal(err)
	}
	question, _, err := store.SendWithWork(ctx, DefaultConversationID, "", "How is it going?", "voice-job-question", nil, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	routine, err := store.AddVoiceRoutine(ctx, DefaultConversationID, call, Routine{
		AgentID: buddy.ID, Name: "Daily check", Prompt: "Check for changes.", Minutes: 1440,
	})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.JoinVoiceGroup(ctx, DefaultConversationID, call, "Buddy & Forge call", forge.ID)
	if err != nil || group.ID == DefaultConversationID {
		t.Fatalf("promote call: %+v, %v", group, err)
	}
	added, err := store.JoinVoiceGroup(ctx, group.ID, call, "", scout.ID)
	if err != nil || added != group {
		t.Fatalf("reuse the call group: %+v, %v", added, err)
	}
	if _, err := store.JoinVoiceGroup(ctx, group.ID, call, "", scout.ID); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, group.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Agent.ID != forge.ID || group.LeadAgentID != forge.ID || len(chat.Members) != 3 || chat.Members[0].ID != buddy.ID || chat.Members[1].ID != forge.ID || chat.Members[2].ID != scout.ID {
		t.Fatalf("group members: %+v", chat.Members)
	}
	if len(chat.Messages) != 1 || chat.Messages[0].Text != "Current call." ||
		len(chat.Work) != 1 || chat.Work[0].ID != work.ID ||
		len(chat.Routines) != 1 || chat.Routines[0].ID != routine.ID {
		t.Fatalf("group did not retain this call: messages=%+v, work=%+v, routines=%+v", chat.Messages, chat.Work, chat.Routines)
	}
	private, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(private.Messages) != 2 || private.Messages[0].Text != "Private call." ||
		len(private.Work) != 0 || len(private.Routines) != 0 {
		t.Fatalf("private conversation after promotion: %+v, %v", private, err)
	}
	if link := private.Messages[1]; link.JobID != work.ID || link.JobConversationID != group.ID {
		t.Fatalf("the original question lost the moved job: %+v", link)
	}
	turn, err := store.turn(ctx, question)
	if err != nil || turn.WorkDetails == nil || turn.WorkDetails.ID != work.ID {
		t.Fatalf("queued question could not read the moved job: %+v, %v", turn.WorkDetails, err)
	}
}

func TestVoiceTeamStartsAfterCallAndReadsPreviousUpdates(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "team.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	buddy, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := store.CreateAgent(ctx, Agent{Name: "Forge"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Team", Members: []string{buddy.ID, peer.ID}, LeadAgentID: peer.ID})
	if err != nil {
		t.Fatal(err)
	}
	const call = "voice-team"
	if err := store.RecordVoiceLine(ctx, group.ID, "older-call", buddy, "user", "An unrelated call in the same group."); err != nil {
		t.Fatal(err)
	}
	if err := store.QueueVoiceTeam(ctx, group.ID, call, "Review the plan", "Review the plan together.", []string{buddy.ID, peer.ID}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); err != ErrNotFound {
		t.Fatalf("work should wait for hangup: %v", err)
	}
	if err := store.RecordVoiceLine(ctx, group.ID, call, buddy, "user", "Focus on the build steps."); err != nil {
		t.Fatal(err)
	}
	// Startup recovery releases held work as well, since the call is gone.
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	wantAgents := []string{buddy.ID, peer.ID, peer.ID}
	updates := []string{"Buddy checked the prerequisites.", "Forge checked the build commands.", "The review is complete."}
	for i, id := range wantAgents {
		task, err := store.claimLane(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.claimLane(ctx, true); err != ErrNotFound {
			t.Fatalf("step %d should block its successor: %v", i, err)
		}
		turn, err := store.turn(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		step := "contribute"
		if i == len(wantAgents)-1 {
			step = "wrap_up"
		}
		if turn.Agent.ID != id || turn.Agent.AllowEdits || turn.TeamStep != step || len(turn.Messages) != i+1 ||
			turn.Messages[0].Text != "Focus on the build steps." {
			t.Fatalf("step %d had the wrong assignment or transcript: %+v", i, turn)
		}
		for j, update := range updates[:i] {
			if turn.Messages[j+1].Text != update {
				t.Fatalf("step %d did not receive step %d's update: %+v", i, j, turn.Messages)
			}
		}
		if err := store.finishTurn(ctx, task.ID, Succeeded, "Finished", updates[i], nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.claimLane(ctx, true); err != ErrNotFound {
		t.Fatalf("the team round should stop after the wrap-up: %v", err)
	}
}
