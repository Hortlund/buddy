package tasks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"buddy/internal/references"
)

func TestFileReferencesRejectAChangedWorkspaceAndFollowManagedMoves(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	bud, err := s.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	original := bud.Workspace
	if err := os.WriteFile(filepath.Join(original, "plan.md"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	task, _, err := s.Send(ctx, DefaultConversationID, "Save a plan", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claim(ctx); err != nil {
		t.Fatal(err)
	}
	bud.Workspace = t.TempDir()
	if err := os.WriteFile(filepath.Join(bud.Workspace, "plan.md"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAgent(ctx, bud.ID, bud); err != nil {
		t.Fatal(err)
	}
	if err := s.finishTurn(ctx, task.ID, Succeeded, "Done", "Saved `plan.md`.", &TurnAction{}); err != nil {
		t.Fatal(err)
	}
	chat, err := s.Conversation(ctx, DefaultConversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := references.Parse(references.File(bud.ID, "plan.md", task.ID))
	if !ok || target.Task != task.ID {
		t.Fatal("file link lost its task binding")
	}
	if got := chat.Messages[len(chat.Messages)-1].Text; !strings.Contains(got, references.File(bud.ID, "plan.md", task.ID)) {
		t.Fatalf("unbound saved reply: %s", got)
	}
	runtime := &CodexRuntime{}
	if _, err := runtime.WorkspaceItem(ctx, s, bud.ID, "", "plan.md", task.ID); !errors.Is(err, ErrWorkspaceFolderChanged) {
		t.Fatalf("a historical link could open the unrelated file: %v", err)
	}
	bud.Workspace = original
	if _, err := s.SaveAgent(ctx, bud.ID, bud); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(parent, "moved-workspaces")
	if err := os.Rename(filepath.Dir(original), newRoot); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableWorkspaces(ctx, newRoot); err != nil {
		t.Fatal(err)
	}
	item, err := runtime.WorkspaceItem(ctx, s, bud.ID, "", "plan.md", task.ID)
	if err != nil || item.Text != "original" {
		t.Fatalf("managed move broke a completed task's link: %+v, %v", item, err)
	}
	if _, err := runtime.WorkspaceItem(ctx, s, bud.ID, "", "plan.md", "tsk_unknown"); err == nil {
		t.Fatal("accepted an unknown workspace binding")
	}
}

func TestWorkStateFeedKeepsCompletionsOutsideTheTeamBudget(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	since := time.Now().Add(-time.Second)
	job, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "own", []Handoff{{Title: "Build service", Brief: "Build the service."}})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.CreateAgent(ctx, Agent{Name: "Researcher"})
	if err != nil {
		t.Fatal(err)
	}
	for batch := range 5 {
		var drafts []Handoff
		for index := range 8 {
			drafts = append(drafts, Handoff{Title: fmt.Sprintf("Research %d/%d", batch, index), Brief: "Research a different topic."})
		}
		if _, err := s.QueueVoiceAssignments(ctx, peer.ConversationID, peer.ID, "other", fmt.Sprint(batch), drafts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE tasks SET status='succeeded',finished=? WHERE id=?", time.Now().UnixMicro(), job[0].ID); err != nil {
		t.Fatal(err)
	}
	team, err := s.TeamContext(ctx, DefaultConversationID, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if !team.Truncated || slices.ContainsFunc(team.Assignments, func(item TeamAssignment) bool { return item.ID == job[0].ID }) {
		t.Fatal("fixture did not exceed the team prompt budget")
	}
	states, err := s.WorkStates(ctx, DefaultConversationID, since)
	if err != nil || len(states) != 1 || states[0].ID != job[0].ID || states[0].Status != Succeeded {
		t.Fatalf("completion disappeared from the notification feed: %+v, %v", states, err)
	}
}

func TestFileReferencesCaptureATeammatesWorkspace(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	peer, err := s.CreateAgent(ctx, Agent{Name: "Designer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peer.Workspace, "design.md"), []byte("design"), 0600); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.Send(ctx, DefaultConversationID, "Show the design", "peer-file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claim(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.finishTurn(ctx, job.ID, Succeeded, "Done", "[Design]("+references.File(peer.ID, "design.md")+")", &TurnAction{}); err != nil {
		t.Fatal(err)
	}
	bound, err := s.ReferenceWorkspace(ctx, peer.ID, job.ID)
	if err != nil || bound != peer.Workspace {
		t.Fatalf("teammate reference lost its workspace: %q, %v", bound, err)
	}
	runtime := &CodexRuntime{}
	item, err := runtime.WorkspaceItem(ctx, s, peer.ID, "", "design.md", job.ID)
	if err != nil || item.Text != "design" || item.SourceTask != job.ID {
		t.Fatalf("could not open the teammate's referenced file: %+v, %v", item, err)
	}
	peer.Workspace = t.TempDir()
	if _, err := s.SaveAgent(ctx, peer.ID, peer); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.WorkspaceItem(ctx, s, peer.ID, "", "design.md", job.ID); !errors.Is(err, ErrWorkspaceFolderChanged) {
		t.Fatalf("teammate reference followed a changed workspace: %v", err)
	}
}
