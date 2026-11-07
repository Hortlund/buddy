package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoveAgentPreservesHistoryAndRetiresItsWork(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	original, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.SaveStandingMemory(ctx, MemoryNote{AgentID: original.ID, Note: "Keep reports concise."})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(original.Workspace, "notes.md")
	if err := os.WriteFile(file, []byte("Keep these notes."), 0600); err != nil {
		t.Fatal(err)
	}
	peer, err := store.CreateAgent(ctx, Agent{Name: "Aster"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Project", Members: []string{original.ID, peer.ID}})
	if err != nil {
		t.Fatal(err)
	}
	routine, err := store.AddRoutine(ctx, group.ID, Routine{AgentID: original.ID, Name: "Daily summary", Prompt: "Check the repo.", Minutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	watch, err := store.CreateWatch(ctx, Watch{ConversationID: DefaultConversationID, Name: "CI", Source: GitHubCI,
		Scope: "acme/repo", Mode: WatchAsk, Instructions: "Check failures.", Budget: 15})
	if err != nil {
		t.Fatal(err)
	}
	signal := StandingSignal{Source: GitHubCI, Scope: watch.Scope, Key: CIWatchKey(123, 1), Title: "Tests failed"}
	if accepted, err := store.RecordStandingSignal(ctx, signal); err != nil || !accepted {
		t.Fatalf("could not create watch approval: %v, %v", accepted, err)
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("watch approval missing: %+v, %v", inbox, err)
	}
	turn, _, err := store.Send(ctx, DefaultConversationID, "Remember this.", "history")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAgent(ctx, original.ID); err == nil {
		t.Fatal("removed a Bud with a queued turn")
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "I remember.", nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.RemoveAgent(ctx, original.ID); err != nil {
			t.Fatal(err)
		}
	}

	dir, err := store.Directory(ctx)
	if err != nil || dir.PrimaryAgentID != peer.ID || len(dir.Agents) != 1 || dir.Agents[0].ID != peer.ID ||
		len(dir.RemovedAgents) != 1 || dir.RemovedAgents[0].ConversationID != DefaultConversationID ||
		dir.RemovedAgents[0].Avatar == nil || dir.RemovedAgents[0].Avatar.Color != original.Avatar.Color {
		t.Fatalf("unexpected directory after removal: %+v, %v", dir, err)
	}
	history, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || !history.Agent.Removed || len(history.Messages) != 2 || history.Messages[1].Text != "I remember." {
		t.Fatalf("removed Bud lost chat history: %+v, %v", history, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "Keep these notes." {
		t.Fatalf("workspace file changed: %q, %v", data, err)
	}
	if _, _, err := store.Send(ctx, DefaultConversationID, "Start new work.", "removed-bud"); err == nil {
		t.Fatal("removed Bud accepted another turn")
	}
	if err := store.SetPrimaryAgent(ctx, original.ID); err == nil {
		t.Fatal("removed Bud became primary")
	}
	if _, err := store.SaveStandingMemory(ctx, MemoryNote{AgentID: original.ID, Note: "New note"}); err == nil {
		t.Fatal("added a memory to a removed Bud")
	}
	memory.Note = "Changed after removal."
	if _, err := store.SaveStandingMemory(ctx, memory); err == nil {
		t.Fatal("edited a removed Bud’s memory")
	}
	if notes, err := store.StandingMemories(ctx, original.ID); err != nil || len(notes) != 1 || notes[0].Note != "Keep reports concise." {
		t.Fatalf("removed Bud’s memory changed: %+v, %v", notes, err)
	}
	project, err := store.Conversation(ctx, group.ID, 0)
	if err != nil || project.Agent.ID != peer.ID || len(project.Members) != 1 || project.Members[0].ID != peer.ID ||
		len(project.Routines) != 1 || project.Routines[0].Enabled {
		t.Fatalf("group was not updated after removal: %+v, %v", project, err)
	}
	enabled := true
	if err := store.UpdateRoutine(ctx, group.ID, routine.ID, &enabled); err == nil {
		t.Fatal("re-enabled the removed Bud's routine")
	}
	if queued, err := store.enqueueDueRoutine(ctx, routine.NextAt.Add(time.Hour)); err != nil || queued {
		t.Fatalf("a removed Bud's routine ran: %v, %v", queued, err)
	}
	if err := store.UpdateInboxItem(ctx, inbox[0].ID, "approve"); !errors.Is(err, ErrInactive) {
		t.Fatalf("removed Bud's watch still accepted approval: %v", err)
	}
	if err := store.SetWatchEnabled(ctx, watch.ID, true); !errors.Is(err, ErrInactive) {
		t.Fatalf("removed Bud's watch could be resumed: %v", err)
	}
	if err := store.RemoveAgent(ctx, peer.ID); err == nil {
		t.Fatal("removed the last active Bud")
	}
	if _, err := store.CreateAgent(ctx, Agent{Name: "Birch"}); err != nil {
		t.Fatal(err)
	}
	groupTurn, _, err := store.Send(ctx, group.ID, "Can we continue?", "group-history")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, groupTurn.ID, Succeeded, "Replied.", "We can.", nil); err != nil {
		t.Fatal(err)
	}
	job, err := store.QueueVoiceWork(ctx, group.ID, peer.ID, "call", "job", "Check the repo", "Report the current state.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, job.ID, Succeeded, "Finished.", "The repo is ready.", nil); err != nil {
		t.Fatal(err)
	}
	if items, err := store.WorkOverview(ctx, group.ID); err != nil || len(items) != 1 || items[0].ChatArchived {
		t.Fatalf("active group reported as archived: %+v, %v", items, err)
	}
	if err := store.RemoveAgent(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	project, err = store.Conversation(ctx, group.ID, 0)
	if err != nil || !project.Agent.Removed || len(project.Members) != 0 || len(project.Messages) != 3 ||
		project.Messages[1].Text != "We can." || project.Messages[1].AgentID != peer.ID {
		t.Fatalf("empty group should still have readable history: %+v, %v", project, err)
	}
	if _, _, err := store.Send(ctx, group.ID, "Start new work.", "empty-group"); err == nil {
		t.Fatal("empty group accepted a new turn")
	}
	if items, err := store.WorkOverview(ctx, ""); err != nil || len(items) != 1 || !items[0].ChatArchived {
		t.Fatalf("work overview lost the archived destination: %+v, %v", items, err)
	}
}

func TestRemovedBudDismissesApprovalFromWatchMovedIntoItsChat(t *testing.T) {
	ctx, path := t.Context(), filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	target, err := store.CreateAgent(ctx, Agent{Name: "Aster"})
	if err != nil {
		t.Fatal(err)
	}
	draft := Watch{ConversationID: DefaultConversationID, Name: "CI", Source: GitHubCI,
		Scope: "acme/moved", Mode: WatchAsk, Instructions: "Investigate failures.", Budget: 15}
	watch, err := store.CreateWatch(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := store.RecordStandingSignal(ctx, StandingSignal{
		Source: watch.Source, Scope: watch.Scope, Key: CIWatchKey(123, 1), Title: "Tests failed",
	}); err != nil || !accepted {
		t.Fatalf("could not create a pending approval: %v, %v", accepted, err)
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("missing watch approval: %+v, %v", inbox, err)
	}
	watch.ConversationID = target.ConversationID
	if _, err := store.UpdateWatch(ctx, watch.ID, watch); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAgent(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateInboxItem(ctx, inbox[0].ID, "approve"); !errors.Is(err, ErrInactive) {
		t.Fatalf("archived watch still accepted an approval: %v", err)
	}

	// Restore the stale state left by the previous server before reopening.
	if _, err := store.db.ExecContext(ctx, "UPDATE standing_inbox SET state='approval',seen=0 WHERE id=?", inbox[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.CreateWatch(ctx, draft)
	if err != nil || restored.ID != watch.ID {
		t.Fatalf("watch was not recreated for the original chat: %+v, %v", restored, err)
	}
	if err := reopened.UpdateInboxItem(ctx, inbox[0].ID, "approve"); !errors.Is(err, ErrInactive) {
		t.Fatalf("recreating a watch revived its old approval: %v", err)
	}
}
