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

func TestBackgroundChoiceAndDelegationPreserveTitlePrivacy(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	other, err := s.CreateAgent(ctx, Agent{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	const request = "Review the release. Ask which checkout to use before starting."
	job, _, err := s.SendMessage(ctx, DefaultConversationID, "release-review", MessageRequest{Text: request, Background: true})
	if err != nil {
		t.Fatal(err)
	}
	finish := func(id, response string) {
		t.Helper()
		claimed, err := s.claimLane(ctx, false)
		if err != nil || claimed.ID != id {
			t.Fatalf("claim planner: %+v, %v", claimed, err)
		}
		turn, err := s.turn(ctx, claimed)
		if err != nil || turn.Origin != backgroundRequest || !slices.ContainsFunc(turn.Messages, func(m Message) bool { return m.Text == request }) {
			t.Fatalf("planner lost the original request: %+v, %v", turn, err)
		}
		turn.action = new(TurnAction)
		reply, err := turn.decodeReply(response, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.finishTurn(ctx, id, Succeeded, "Replied.", reply, turn.action); err != nil {
			t.Fatal(err)
		}
	}
	finish(job.ID, `{"reply":"","card":{"question":"Which checkout?","options":["Stable","Beta"]},"handoff":{"title":"Premature handoff","brief":"Review the release"}}`)
	chat, err := s.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Work) != 0 || len(chat.Messages) != 2 || chat.Messages[1].Card == nil {
		t.Fatalf("planner did not wait for the choice: %+v, %v", chat, err)
	}
	answer, _, err := s.AnswerCard(ctx, DefaultConversationID, job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	finish(answer.ID, `{"reply":"","handoffs":[{"title":"Private release","brief":"Review the stable checkout"}]}`)
	root, err := s.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.controlWork(ctx, root.ID, "delegate", json.RawMessage(`{"request_id":"child","title":"Private subtask","brief":"Check the release logs","read_only":true}`)); err != nil {
		t.Fatal(err)
	}
	team, err := s.TeamContext(ctx, other.ConversationID, other.ID)
	if err != nil || len(team.Assignments) != 2 || slices.ContainsFunc(team.Assignments, func(item TeamAssignment) bool { return item.Title != "Background assignment" }) {
		t.Fatalf("shared team context leaked a title: %+v, %v", team.Assignments, err)
	}
	work, err := s.WorkContext(ctx, other.ConversationID, DefaultAgentID)
	if err != nil || len(work) != 2 || slices.ContainsFunc(work, func(item WorkItem) bool { return item.Title != "Background assignment" }) {
		t.Fatalf("shared work context leaked a title: %+v, %v", work, err)
	}
	work, err = s.WorkOverview(ctx, DefaultConversationID)
	if err != nil || len(work) != 2 || !slices.ContainsFunc(work, func(item WorkItem) bool { return item.Title == "Private subtask" }) {
		t.Fatalf("original chat lost its titles: %+v, %v", work, err)
	}
}

func TestTypedBackgroundClarificationKeepsRequestAndRouting(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	other, err := s.CreateAgent(ctx, Agent{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	const request = "Review the attached release. Ask me which build to use before starting."
	const question = "What build identifier should I use?"
	plan, _, err := s.SendMessage(ctx, DefaultConversationID, "plan-review", MessageRequest{
		Text: request, Background: true, Attachments: []Attachment{{Name: "release.txt", Data: []byte("release notes")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	finish := func(id, response string) Turn {
		t.Helper()
		claimed, err := s.claimLane(ctx, false)
		if err != nil || claimed.ID != id {
			t.Fatalf("claim planner: %+v, %v", claimed, err)
		}
		turn, err := s.turn(ctx, claimed)
		if err != nil || turn.Origin != backgroundRequest || !turn.coordinate {
			t.Fatalf("unexpected planner turn: %+v, %v", turn, err)
		}
		turn.action = new(TurnAction)
		reply, err := turn.decodeReply(response, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.finishTurn(ctx, id, Succeeded, "Replied.", reply, turn.action); err != nil {
			t.Fatal(err)
		}
		return turn
	}
	finish(plan.ID, `{"reply":"","clarification":"What build identifier should I use?","handoffs":[{"title":"Too soon","brief":"Review the release"}]}`)
	chat, err := s.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Work) != 0 || len(chat.Messages) != 2 || !chat.Messages[1].BackgroundQuestion || chat.Messages[1].Text != question {
		t.Fatalf("missing pending background question: %+v, %v", chat, err)
	}
	input := MessageRequest{BackgroundAnswer: plan.ID,
		Attachments: []Attachment{{Name: "manifest.txt", Data: []byte("build manifest")}}}
	if _, _, err := s.SendMessage(ctx, other.ConversationID, "cross-chat", input); err == nil {
		t.Fatal("another chat answered the background question")
	}
	answer, _, err := s.SendMessage(ctx, DefaultConversationID, "answer-build", input)
	if err != nil {
		t.Fatal(err)
	}
	if retry, created, err := s.SendMessage(ctx, DefaultConversationID, "answer-build", input); err != nil || created || retry.ID != answer.ID {
		t.Fatalf("retry did not return the original turn: %+v, %v, %v", retry, created, err)
	}
	if _, _, err := s.SendMessage(ctx, DefaultConversationID, "second-answer", input); err == nil {
		t.Fatal("another turn answered the same question")
	}
	turn := finish(answer.ID, `{"reply":"","handoffs":[{"title":"Private build v1.2","brief":"Review the attached release for v1.2"}]}`)
	if !slices.ContainsFunc(turn.Messages, func(m Message) bool { return m.Text == request }) ||
		!slices.ContainsFunc(turn.Messages, func(m Message) bool { return m.Text == input.Text && m.AnswerTo == question }) ||
		len(turn.Attachments) != 2 || turn.Attachments[0].Name != "release.txt" || turn.Attachments[1].Name != "manifest.txt" {
		t.Fatalf("planner lost the original request or the linked answer: %+v", turn)
	}
	job, err := s.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	workerTurn, err := s.turn(ctx, job)
	if err != nil || len(workerTurn.Attachments) != 2 ||
		string(workerTurn.Attachments[0].Data) != "release notes" || string(workerTurn.Attachments[1].Data) != "build manifest" {
		t.Fatalf("worker lost clarification files: %+v, %v", workerTurn.Attachments, err)
	}
	work, err := s.WorkContext(ctx, other.ConversationID, DefaultAgentID)
	if err != nil || len(work) != 1 || work[0].Title != "Background assignment" {
		t.Fatalf("resumed plan leaked the title: %+v, %v", work, err)
	}
}

func TestBackgroundClarificationReopensAfterCancelledOrFailedAnswer(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	plan, _, err := s.SendMessage(ctx, DefaultConversationID, "plan", MessageRequest{Text: "Review the build.", Background: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s.finishTurn(ctx, plan.ID, Succeeded, "Replied.", "Which build?", &TurnAction{backgroundQuestion: true}); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		key     string
		outcome Status
		result  string
	}{
		{"cancelled", Cancelled, ""},
		{"failed", Failed, ""},
		{"invalid-worker", Failed, `{"reply":"","handoffs":[{"agent_id":"agt_missing","title":"Review","brief":"Review the build."}]}`},
		{"empty-promise", Failed, `{"reply":"Thanks, I’ll start that now."}`},
		{"succeeded", Succeeded, `{"reply":"","handoffs":[{"title":"Review","brief":"Review the build at /tmp/build."}]}`},
	} {
		key, outcome := attempt.key, attempt.outcome
		input := MessageRequest{Text: "/tmp/build", BackgroundAnswer: plan.ID}
		answer, _, err := s.SendMessage(ctx, DefaultConversationID, key, input)
		if err != nil {
			t.Fatal(err)
		}
		chat, err := s.Conversation(ctx, DefaultConversationID, 0)
		if err != nil || len(chat.OpenBackgroundQuestions) != 0 {
			t.Fatalf("accepted answer didn't reserve the question: %v, %v", chat.OpenBackgroundQuestions, err)
		}
		if outcome == Cancelled {
			if _, err := s.cancel(ctx, answer.ID, true); err != nil {
				t.Fatal(err)
			}
		} else {
			claimed, err := s.claimLane(ctx, false)
			if err != nil || claimed.ID != answer.ID {
				t.Fatalf("claim answer: %+v, %v", claimed, err)
			}
			if attempt.result != "" {
				turn, err := s.turn(ctx, claimed)
				if err != nil || turn.Skill != nil {
					t.Fatalf("path reply was not a literal answer: %+v, %v", turn.Skill, err)
				}
				turn.action = new(TurnAction)
				reply, err := turn.decodeReply(attempt.result, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.finishTurn(ctx, answer.ID, Succeeded, "Replied.", reply, turn.action); err != nil {
					t.Fatal(err)
				}
			} else if err := s.finish(ctx, answer.ID, outcome, "Finished.", ""); err != nil {
				t.Fatal(err)
			}
		}
		if saved, err := s.Get(ctx, answer.ID); err != nil || saved.Status != outcome {
			t.Fatalf("answer status = %s, want %s: %v", saved.Status, outcome, err)
		}
		chat, err = s.Conversation(ctx, DefaultConversationID, 0)
		if err != nil || slices.Contains(chat.OpenBackgroundQuestions, plan.ID) != (outcome != Succeeded) {
			t.Fatalf("question state after %s: %v, %v", outcome, chat.OpenBackgroundQuestions, err)
		}
		if retry, created, err := s.SendMessage(ctx, DefaultConversationID, key, input); err != nil || created || retry.ID != answer.ID {
			t.Fatalf("retried request lost its original answer: %+v, %v, %v", retry, created, err)
		}
	}
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
