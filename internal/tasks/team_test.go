package tasks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"buddy/internal/references"
)

func teamTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.Context(), filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableWorkspaces(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTeamContextFollowsNamesLeadershipAndDepartures(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	peer, err := s.CreateAgent(ctx, Agent{Name: "Aster", Role: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateGroup(ctx, GroupRequest{Name: "Workshop", Members: []string{DefaultAgentID, peer.ID}, LeadAgentID: peer.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrimaryAgent(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	peer.Name, peer.Role = "Orbit", "Architect"
	if _, err := s.SaveAgent(ctx, peer.ID, peer); err != nil {
		t.Fatal(err)
	}
	team, err := s.TeamContext(ctx, group.ID, DefaultAgentID)
	if err != nil || team.PrimaryID != peer.ID || team.LeadID != peer.ID || len(team.Members) != 2 ||
		!slices.ContainsFunc(team.Buds, func(bud AgentRef) bool { return bud.ID == peer.ID && bud.Name == "Orbit" && bud.Role == "Architect" }) {
		t.Fatalf("stale team context: %+v, %v", team, err)
	}
	if err := s.RemoveAgent(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	team, err = s.TeamContext(ctx, group.ID, DefaultAgentID)
	if err != nil || team.PrimaryID != DefaultAgentID || team.LeadID != DefaultAgentID || len(team.Members) != 1 || len(team.Buds) != 1 {
		t.Fatalf("departure left a stale leader or member: %+v, %v", team, err)
	}
	if !slices.ContainsFunc(team.Changes, func(event TeamEvent) bool {
		return event.Kind == "left_team" && event.AgentID == peer.ID && event.Name == "Orbit"
	}) {
		t.Fatal("teammates cannot see the departure")
	}
}

func TestChatAndWorkersKnowTheTeamWithoutPrivateTranscripts(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	peer, err := s.CreateAgent(ctx, Agent{Name: "Nova", Role: "Reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateGroup(ctx, GroupRequest{Name: "Team", Members: []string{DefaultAgentID, peer.ID}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.QueueVoiceAssignments(ctx, group.ID, DefaultAgentID, "call", "batch", []Handoff{
		{Title: "Build the service", Brief: "Build an HTTP service."},
		{AgentID: peer.ID, Title: "Review the service", Brief: "Review the builder's result.", ReadOnly: true, After: []int{1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	private, err := s.QueueVoiceAssignments(ctx, peer.ConversationID, peer.ID, "private-call", "batch", []Handoff{
		{Title: "Private research", Brief: "PRIVATE ASSIGNMENT DETAILS"},
	})
	if err != nil {
		t.Fatal(err)
	}
	question, _, err := s.Send(ctx, group.ID, "Who is doing what?", "question")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []Task{question, jobs[0].Task, jobs[1].Task} {
		turn, err := s.turn(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		if turn.BuddyTeam.LeadID != DefaultAgentID || turn.BuddyTeam.CurrentTaskID != task.ID || len(turn.BuddyTeam.Assignments) != 3 {
			t.Fatalf("chat or worker lacked shared context: %+v", turn.BuddyTeam)
		}
		var dependent, elsewhere bool
		for _, job := range turn.BuddyTeam.Assignments {
			dependent = dependent || job.ID == jobs[1].ID && slices.Equal(job.After, []string{jobs[0].ID})
			elsewhere = elsewhere || job.ID == private[0].ID && !job.InThisChat
		}
		data, _ := json.Marshal(turn.BuddyTeam)
		if !dependent || !elsewhere || strings.Contains(string(data), "PRIVATE ASSIGNMENT DETAILS") {
			t.Fatalf("missing dependency/other status, or private details leaked: %s", data)
		}
	}
}

func TestRepeatedAssignmentsReuseActiveWorkButAllowChangedWork(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	assignment := Handoff{Title: "Build service", Brief: "Implement server.go"}
	queue := func(key string, draft Handoff) AssignedWork {
		t.Helper()
		jobs, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", key, []Handoff{draft})
		if err != nil || len(jobs) != 1 {
			t.Fatalf("queue: %+v, %v", jobs, err)
		}
		return jobs[0]
	}
	first := queue("first", assignment)
	reused := queue("repeat", assignment)
	if reused.ID != first.ID || !reused.Reused {
		t.Fatalf("started duplicate work: %+v, %+v", first, reused)
	}
	changed := assignment
	changed.Brief = "Implement Server.go"
	if next := queue("changed", changed); next.ID == first.ID || next.Reused {
		t.Fatal("case-sensitive file assignments were incorrectly combined")
	}
	if _, err := s.cancel(ctx, first.ID, false); err != nil {
		t.Fatal(err)
	}
	if retry := queue("repeat", assignment); retry.ID != first.ID {
		t.Fatal("retrying a reused request created another job")
	}
	if next := queue("after-stop", assignment); next.ID == first.ID || next.Reused {
		t.Fatal("stopped work could not be requested again")
	}
}

func TestQueuedGroupTurnUsesTheCurrentLead(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	peer, err := s.CreateAgent(ctx, Agent{Name: "Nova", Role: "Coordinator"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateGroup(ctx, GroupRequest{Name: "Team", Members: []string{DefaultAgentID, peer.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, group.ID, "What should we do next?", "question"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupLead(ctx, group.ID, peer.ID); err != nil {
		t.Fatal(err)
	}
	task, err := s.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err = NewWorker(s, nil, nil).routeGroupTurn(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.turn(ctx, task)
	if err != nil || turn.Agent.ID != peer.ID || turn.BuddyTeam.LeadID != peer.ID {
		t.Fatalf("a queued open question went to the old lead: %+v, %v", turn.Agent, err)
	}
}

func TestReusedCallWorkFollowsTheCallIntoItsGroup(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	peer, err := s.CreateAgent(ctx, Agent{Name: "Nova"})
	if err != nil {
		t.Fatal(err)
	}
	draft := []Handoff{{Title: "Build service", Brief: "Build the service."}}
	first, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "earlier-call", "work", draft)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "current-call", "work", draft)
	if err != nil || reused[0].ID != first[0].ID {
		t.Fatalf("did not reuse ongoing work: %+v, %v", reused, err)
	}
	group, err := s.JoinVoiceGroup(ctx, DefaultConversationID, "current-call", "Team call", peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkDetails(ctx, group.ID, first[0].ID); err != nil {
		t.Fatal("ongoing work was left behind in the private chat:", err)
	}
	if _, err := s.WorkDetails(ctx, DefaultConversationID, first[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a migrated job is still bound to the old chat:", err)
	}
	resolved, err := s.ReferencedWorkDetails(ctx, first[0].ID)
	if err != nil || resolved.ConversationID != group.ID {
		t.Fatalf("a saved job link could not resolve its new group: %+v, %v", resolved, err)
	}
}

func TestRepliesLinkCreatedWorkspaceFiles(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	bud, err := s.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bud.Workspace, "plan.md"), []byte("# Plan"), 0600); err != nil {
		t.Fatal(err)
	}
	task, _, err := s.Send(ctx, DefaultConversationID, "Save a plan", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claim(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.finishTurn(ctx, task.ID, Succeeded, "Done", "Saved `plan.md`.", &TurnAction{}); err != nil {
		t.Fatal(err)
	}
	chat, err := s.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 2 || !strings.Contains(chat.Messages[1].Text, references.File(DefaultAgentID, "plan.md")) {
		t.Fatalf("the saved reply has no native file link: %+v, %v", chat.Messages, err)
	}
}
