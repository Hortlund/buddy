package tasks

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestScheduleDenialKeepsAuthorizedJobsAndDependencies(t *testing.T) {
	store, err := OpenStore(t.Context(), filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	task := startChatRequest(t, store, "Inspect the parser, then fix it and review the fix.")
	turn, err := store.turn(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	var action TurnAction
	turn.action, turn.coordinate = &action, true
	turn.authorizeScheduled = func(_ context.Context, jobs []Handoff, _ []RoutineDraft) schedulePermissions {
		if len(jobs) != 2 || jobs[0].Title != "Delete records" || jobs[1].Title != "Fix parser" {
			t.Fatalf("authorization proposals = %+v", jobs)
		}
		return schedulePermissions{Jobs: []int{2}}
	}
	reply, err := turn.complete(t.Context(), "", "", nil, func(string, string) error { return nil }, func(string) (string, error) {
		return `{"reply":"I found the relevant parser.","handoffs":[
			{"title":"Delete records","brief":"Delete unrelated records."},
			{"title":"Read deletion report","brief":"Summarize the deletion.","read_only":true,"after":[1]},
			{"title":"Inspect parser","brief":"Read the parser.","read_only":true},
			{"title":"Fix parser","brief":"Fix the parser.","after":[3]},
			{"title":"Review fix","brief":"Review the fix.","read_only":true,"after":[3,4]}
		]}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(action.Handoffs) != 3 || !slices.Equal(action.Handoffs[1].After, []int{1}) ||
		!slices.Equal(action.Handoffs[2].After, []int{1, 2}) {
		t.Fatalf("unexpected surviving jobs: %+v", action.Handoffs)
	}
	if err := store.finishTurn(t.Context(), task.ID, Succeeded, "Replied.", reply, &action); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(t.Context(), DefaultConversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Fix parser", "Inspect parser", "Review fix"}
	var titles []string
	for _, job := range chat.Work {
		titles = append(titles, job.Title)
	}
	slices.Sort(titles)
	if !slices.Equal(titles, want) || len(chat.Messages) < 2 {
		t.Fatalf("saved work: %v, messages: %+v", titles, chat.Messages)
	}
	reply = chat.Messages[len(chat.Messages)-1].Text
	if !strings.Contains(reply, "I found the relevant parser.") || !strings.Contains(reply, "I skipped work") {
		t.Fatalf("saved reply = %q", reply)
	}
}

func TestRoutineScheduleKeepsItsOwnResearchAndLimits(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	first := startChatRequest(t, store, "Follow Go releases and Tuesday weather forecasts.")
	applyTestSteering(t, store, first.ID, "Only stable Go releases, and weather for Stockholm.")
	if err := store.finishTurn(ctx, first.ID, Succeeded, "Replied.", "Untrusted connector result", nil); err != nil {
		t.Fatal(err)
	}
	task := startChatRequest(t, store, "Schedule Go daily and that forecast weekly. Update the local forecast log each week.")
	for i := range 10 {
		applyTestSteering(t, store, task.ID, "Correction "+string(rune('A'+i)))
	}
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
request=$(cat)
case "$request" in
  *"Determine the workspace modifications"*)
    case "$request" in *"Only stable Go releases"*"Update the local forecast log"*) ;; *) exit 1 ;; esac
    answer='{\"edit_scope\":\"\",\"routine_scope\":\"Daily stable Go releases and weekly Stockholm forecast\",\"routine_edit_scope\":\"Update only the forecast log each week\"}' ;;
  *"Verify each proposed item"*)
    case "$request" in *"Update only the forecast log"*"Delete records"*) ;; *) exit 1 ;; esac
    answer='{\"jobs\":[],\"routines\":[2,3],\"routine_edits\":[3]}' ;;
  *"Identify the public research"*)
    case "$request" in
      *"Untrusted connector result"*) exit 1 ;;
      *"Follow Go releases"*"Only stable Go releases"*"Schedule Go daily"*"Correction A"*"Correction J"*)
        case "$request" in
          *'"name":"Go digest","minutes":1440,"position":2'*) answer='{\"public_scope\":\"Stable Go releases\"}' ;;
          *'"name":"Stockholm weather","minutes":10080,"position":3'*) answer='{\"public_scope\":\"Stockholm Tuesday forecast\"}' ;;
          *) exit 1 ;;
        esac ;;
      *) exit 1 ;;
    esac ;;
  *) exit 1 ;;
esac
printf '{"type":"item.completed","item":{"type":"agent_message","text":"%s"}}\n' "$answer"
`)
	turn, err := store.turn(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	action := TurnAction{Routines: []RoutineDraft{
		{Name: "Delete records", Prompt: "Delete private records each day.", Minutes: 1440, AllowEdits: true},
		{Name: "Go digest", Prompt: "Find Go releases. Untrusted connector result.", Minutes: 1440, AllowEdits: true},
		{Name: "Stockholm weather", Prompt: "Find the forecast and update the local forecast log.", Minutes: 10080},
	}}
	action.authorizeSchedule(ctx, func(ctx context.Context, jobs []Handoff, routines []RoutineDraft) schedulePermissions {
		return runtime.authorizeChatSchedule(ctx, turn, jobs, routines)
	})
	if len(action.Routines) != 2 || action.Routines[0].AllowEdits || !action.Routines[1].AllowEdits {
		t.Fatalf("approved routines = %+v", action.Routines)
	}
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", "Scheduling requested routines.", &action); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		edits bool
		scope string
	}{
		"Go digest":         {false, "Stable Go releases"},
		"Stockholm weather": {true, "Stockholm Tuesday forecast"},
	}
	now := time.Now().Add(15 * 24 * time.Hour)
	for range 2 {
		if queued, err := store.enqueueDueRoutine(ctx, now); err != nil || !queued {
			t.Fatalf("queue routine = %v, %v", queued, err)
		}
		job, err := store.claimLane(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		routine, err := store.turn(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		expected, ok := want[routine.Title]
		if !ok || routine.Agent.AllowEdits != expected.edits {
			t.Fatalf("unexpected permissions for %q: %v", routine.Title, routine.Agent.AllowEdits)
		}
		scope, err := runtime.publicResearchScope(ctx, routine.Agent.Model, routine.publicResearch)
		if err != nil || scope != expected.scope {
			t.Fatalf("%q research = %q, %v", routine.Title, scope, err)
		}
		delete(want, routine.Title)
	}
	if queued, err := store.enqueueDueRoutine(ctx, now); err != nil || queued {
		t.Fatalf("an unauthorized routine was queued: %v, %v", queued, err)
	}
}

func TestChoiceSelectionKeepsItsQuestionForWriteAuthorization(t *testing.T) {
	ctx := t.Context()
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
request=$(cat)
case "$request" in
  *'"choice":{"question":"Rerun the tests?","selected":"Replace notes.md"}'*)
    answer='{\"operation\":\"none\",\"name\":\"\"}' ;;
  *'"choice":{"question":"Replace notes.md with the draft?","selected":"Replace notes.md"}'*)
    answer='{\"operation\":\"replace\",\"name\":\"notes.md\"}' ;;
  *) exit 1 ;;
esac
printf '{"type":"item.completed","item":{"type":"agent_message","text":"%s"}}\n' "$answer"
`)
	for _, tc := range []struct {
		question string
		allowed  bool
	}{{"Rerun the tests?", false}, {"Replace notes.md with the draft?", true}} {
		store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		task := startChatRequest(t, store, "Prepare the draft and show my choices.")
		action := &TurnAction{Card: &ChoiceCard{Question: tc.question, Options: []string{"Replace notes.md", "Leave it"}}}
		if err := store.finishTurn(ctx, task.ID, Succeeded, "Asked.", "", action); err != nil {
			t.Fatal(err)
		}
		answer, _, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if answer, err = store.claim(ctx); err != nil {
			t.Fatal(err)
		}
		turn, err := store.turn(ctx, answer)
		if err != nil {
			t.Fatal(err)
		}
		history, err := turn.authorizationHistory(ctx)
		if err != nil || len(history) == 0 {
			t.Fatalf("history = %+v, %v", history, err)
		}
		last, _ := json.Marshal(history[len(history)-1])
		if !strings.Contains(string(last), tc.question) || strings.Contains(string(last), `"user":`) {
			t.Fatalf("choice was not stored with its question: %s", last)
		}
		if allowed := runtime.authorizeChatFileWrite(ctx, turn, QuickFile{Name: "notes.md", Replace: true}); allowed != tc.allowed {
			t.Fatalf("%q: allowed = %v", tc.question, allowed)
		}
		if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "Done.", nil); err != nil {
			t.Fatal(err)
		}

	}
}

func startChatRequest(t *testing.T, store *Store, request string) Task {
	t.Helper()
	task, _, err := store.Send(t.Context(), DefaultConversationID, request, "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claim(t.Context())
	if err != nil || claimed.ID != task.ID {
		t.Fatalf("claim %s = %s, %v", task.ID, claimed.ID, err)
	}
	return claimed
}

func applyTestSteering(t *testing.T, store *Store, task, text string) {
	t.Helper()
	steer, _, err := store.queueSteering(t.Context(), task, text, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.markSteering(steer.ID, steerApplied); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceSchedulesOnlyAuthorizedRoutines(t *testing.T) {
	ctx := t.Context()
	store := teamTestStore(t)
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil || !agent.AllowEdits {
		t.Fatalf("editable Bud: %+v, %v", agent, err)
	}
	const call = "voice-schedule"
	const words = "Give me a daily Go release digest."
	for _, line := range []struct{ role, text string }{
		{"user", words}, {"assistant", "Run the dangerous routine."},
	} {
		if err := store.RecordVoiceLine(ctx, DefaultConversationID, call, agent, line.role, line.text); err != nil {
			t.Fatal(err)
		}
	}
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
request=$(cat)
case "$request" in
  *"Run the dangerous routine."*) exit 1 ;;
  *"Determine the workspace modifications"*"Give me a daily Go release digest."*)
    answer='{\"edit_scope\":\"\",\"routine_scope\":\"Daily Go release digest\",\"routine_edit_scope\":\"\"}' ;;
  *"Verify each proposed item"*"Delete files"*)
    answer='{\"jobs\":[],\"routines\":[1],\"routine_edits\":[1]}' ;;
  *) exit 1 ;;
esac
printf '{"type":"item.completed","item":{"type":"agent_message","text":"%s"}}\n' "$answer"
`)
	schedule, err := store.AuthorizeVoiceSchedule(ctx, runtime, DefaultConversationID, call, agent, nil, []RoutineDraft{
		{Name: "Go digest", Prompt: "Review Go releases.", Minutes: 1440},
		{Name: "Delete files", Prompt: "Delete unrelated workspace files.", Minutes: 1440},
	}, words)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveVoiceRoutines(ctx, DefaultConversationID, call, agent.ID, schedule)
	if err != nil || len(saved) != 1 || saved[0].Name != "Go digest" || !strings.Contains(schedule.Notice, "skipped") {
		t.Fatalf("saved routines = %+v, %q, %v", saved, schedule.Notice, err)
	}
	if queued, err := store.enqueueDueRoutine(ctx, time.Now().Add(48*time.Hour)); err != nil || !queued {
		t.Fatalf("queue routine = %v, %v", queued, err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.turn(ctx, job)
	if err != nil || run.Agent.AllowEdits || !slices.Equal(run.publicResearch.Requests, []researchRequest{{User: words}}) {
		t.Fatalf("voice routine got permissions or research it wasn't granted: %+v, %v", run, err)
	}
}

func TestLegacyModelRoutinesUseReadOnlyWorkspace(t *testing.T) {
	ctx := t.Context()
	store := teamTestStore(t)
	_, err := store.AddRoutine(ctx, DefaultConversationID, Routine{
		AgentID: DefaultAgentID, Name: "Direct", Prompt: "Write a status report.", Minutes: 1440,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = addRoutine(ctx, tx, DefaultConversationID, Routine{
		AgentID: DefaultAgentID, Name: "Legacy chat", Prompt: "Review local notes.", Minutes: 1440,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Direct": true, "Legacy chat": false}
	for range 2 {
		if queued, err := store.enqueueDueRoutine(ctx, time.Now().Add(48*time.Hour)); err != nil || !queued {
			t.Fatalf("queue routine = %v, %v", queued, err)
		}
		job, err := store.claimLane(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		run, err := store.turn(ctx, job)
		if allowed, ok := want[run.Title]; err != nil || !ok || run.Agent.AllowEdits != allowed {
			t.Fatalf("legacy routine %q can edit = %v, %v", run.Title, run.Agent.AllowEdits, err)
		}
		delete(want, run.Title)
	}
}

func TestReopeningStoreRestrictsQueuedLegacyRoutine(t *testing.T) {
	ctx := t.Context()
	store := teamTestStore(t)
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil || !agent.AllowEdits {
		t.Fatalf("writable test workspace: %v, %v", agent.AllowEdits, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	routine, err := addRoutine(ctx, tx, DefaultConversationID, Routine{
		AgentID: agent.ID, Name: "Legacy routine", Prompt: "Review local notes.", Minutes: 1440,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := enqueueTurn(ctx, tx, BackgroundWork, "legacy-occurrence", DefaultConversationID, agent, workRequest{
		origin: "routine", routine: routine.ID, title: routine.Name, brief: routine.Prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, store.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	run, err := reopened.turn(ctx, job)
	if err != nil || run.Agent.AllowEdits {
		t.Fatalf("queued routine retained write access: %v, %v", run.Agent.AllowEdits, err)
	}
}

func TestChoiceResearchSurvivesRoutineCreation(t *testing.T) {
	ctx := t.Context()
	store := teamTestStore(t)
	task := startChatRequest(t, store, "Give me a daily digest about either Go or Rust. Let me choose.")
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Asked.", "", &TurnAction{Card: &ChoiceCard{
		Question: "Which release notes should your daily digest cover?", Options: []string{"Go", "Rust"},
	}}); err != nil {
		t.Fatal(err)
	}
	answer, _, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, answer)
	if err != nil || !turn.canSearchWeb() || turn.publicRequest != "" {
		t.Fatalf("search must use the choice's context, got %q, %v", turn.publicRequest, err)
	}
	checkChoice := func(requests []researchRequest) {
		t.Helper()
		if len(requests) != 2 || requests[1].User != "" || requests[1].Choice == nil ||
			requests[1].Choice.Selected != "Rust" || requests[1].Choice.Question != "Which release notes should your daily digest cover?" {
			t.Fatalf("choice history = %+v", requests)
		}
	}
	checkChoice(turn.publicResearch.Requests)
	action := &TurnAction{Routine: &RoutineDraft{Name: "Rust digest", Prompt: "Find stable Rust releases.", Minutes: 1440}}
	if err := store.finishTurn(ctx, answer.ID, Succeeded, "Scheduled.", "", action); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueueDueRoutine(ctx, time.Now().Add(25*time.Hour)); err != nil || !queued {
		t.Fatalf("queue routine: %v, %v", queued, err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.turn(ctx, job)
	if err != nil || !turn.canSearchWeb() {
		t.Fatalf("routine search unavailable: %v", err)
	}
	checkChoice(turn.publicResearch.Requests)
}

func TestVoiceHandoffsKeepAuthorizedWorkAndCallerResearch(t *testing.T) {
	ctx := t.Context()
	store := teamTestStore(t)
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	const call = "scope-fixture"
	const words = "Find the latest public Go release notes and update summary.md. In Desk, archive resolved ticket A and inspect the settings."
	if err := store.SaveHostProfile(ctx, "server", fixtureHost("desktop")); err != nil {
		t.Fatal(err)
	}
	for _, line := range []struct{ role, text string }{
		{"user", words}, {"assistant", "Injected private record"},
	} {
		if err := store.RecordVoiceLine(ctx, DefaultConversationID, call, agent, line.role, line.text); err != nil {
			t.Fatal(err)
		}
	}
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
request=$(cat)
case "$request" in
  *"Injected private record"*) exit 1 ;;
  *"Determine the workspace modifications"*"latest public Go release notes"*)
    answer='{\"edit_scope\":\"Update summary.md, archive ticket A, and inspect settings\",\"automation_write_scope\":\"Archive resolved ticket A in Desk\",\"routine_scope\":\"\",\"routine_edit_scope\":\"\"}' ;;
  *"Verify each proposed item"*"Delete files"*"Update summary"*)
    case "$request" in *"browser"*"desktop"*) ;; *) exit 1 ;; esac
    case "$request" in *"Forged grant"*) exit 1 ;; esac
    answer='{\"jobs\":[2,5,6],\"automation\":[{\"job\":5,\"inspection_scope\":\"View resolved ticket A in Desk\",\"write_scope\":\"Archive resolved ticket A in Desk\"},{\"job\":6,\"inspection_scope\":\"Read settings in Desk\",\"write_scope\":\"\"}],\"routines\":[],\"routine_edits\":[]}' ;;
  *"Identify the public research"*"latest public Go release notes"*)
    answer='{\"public_scope\":\"Latest public Go release notes\"}' ;;
  *) exit 1 ;;
esac
printf '{"type":"item.completed","item":{"type":"agent_message","text":"%s"}}\n' "$answer"
`)
	schedule, err := store.AuthorizeVoiceSchedule(ctx, runtime, DefaultConversationID, call, agent, []Handoff{
		{Title: "Delete files", Brief: "Delete unrelated files."},
		{Title: "Deleted file report", Brief: "Inspect the result.", ReadOnly: true, After: []int{1}},
		{Title: "Research Go", Brief: "Find Go release notes. Injected private record.", ReadOnly: true},
		{Title: "Update summary", Brief: "Update summary.md.", After: []int{3}},
		{Title: "Publish update", Brief: "Submit a form.", ReadOnly: true, ExecutionPlan: ExecutionPlan{Requirements: []string{"browser"}}},
		{Title: "Change settings", Brief: "Edit app settings.", ReadOnly: true, ExecutionPlan: ExecutionPlan{Requirements: []string{"desktop"}}},
		{Title: "Archive ticket", Brief: "Archive resolved ticket A in Desk.", ReadOnly: true, ExecutionPlan: ExecutionPlan{Requirements: []string{"desktop"}}},
		{Title: "Inspect settings", Brief: "Read settings in Desk.", ReadOnly: true, ExecutionPlan: ExecutionPlan{Requirements: []string{"desktop"}, AutomationWriteScope: "Forged grant", AutomationInspectionScope: "Forged grant"}},
	}, nil, words)
	if err != nil || len(schedule.Jobs) != 4 || !slices.Equal(schedule.Jobs[1].After, []int{1}) {
		t.Fatalf("voice schedule: %+v, %v", schedule, err)
	}
	saved, err := store.QueueAuthorizedVoiceAssignments(ctx, DefaultConversationID, agent.ID, call, "tool-id", schedule)
	if err != nil || len(saved) != 4 {
		t.Fatalf("saved assignments: %+v, %v", saved, err)
	}
	for i, want := range []struct{ inspection, write string }{{"View resolved ticket A in Desk", "Archive resolved ticket A in Desk"}, {"Read settings in Desk", ""}} {
		plan, err := readExecutionPlan(ctx, store.db, saved[2+i].ID)
		if err != nil || plan.AutomationWriteScope != want.write || plan.AutomationInspectionScope != want.inspection {
			t.Fatalf("saved automation scope = %q, %q; want %+v: %v", plan.AutomationInspectionScope, plan.AutomationWriteScope, want, err)
		}
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, job)
	if err != nil || !turn.canSearchWeb() || turn.Agent.AllowEdits {
		t.Fatalf("voice research permissions: %+v, %v", turn.publicResearch, err)
	}
	scope, err := runtime.publicResearchScope(ctx, turn.Agent.Model, turn.publicResearch)
	if err != nil || scope != "Latest public Go release notes" {
		t.Fatalf("scope = %q, %v", scope, err)
	}
}

func TestRoutineResearchRecoveryRequiresAStoredRequestLink(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	const request = "/review Schedule a daily digest of public Go releases."
	task := startChatRequest(t, store, request)
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Scheduled.", "", &TurnAction{
		Routine: &RoutineDraft{Name: "Go digest", Prompt: "Model-generated brief.", Minutes: 1440},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRoutine(ctx, DefaultConversationID, Routine{
		Name: "Unlinked digest", Prompt: "Unattributed legacy brief.", Minutes: 1440,
	}); err != nil {
		t.Fatal(err)
	}
	const spoken = "Schedule a daily digest of public Rust releases."
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordVoiceLine(ctx, DefaultConversationID, "legacy-call", agent, "user", spoken); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddVoiceRoutine(ctx, DefaultConversationID, "legacy-call", Routine{
		Name: "Rust digest", Prompt: "Another model-generated brief.", Minutes: 1440,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM routine_public_requests"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, store.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Routines) != 3 {
		t.Fatalf("routines: %+v, %v", chat.Routines, err)
	}
	for _, routine := range chat.Routines {
		if routine.ResearchScopeMissing != (routine.Name == "Unlinked digest") {
			t.Fatalf("unexpected research availability: %+v", routine)
		}
	}
	want := map[string]string{"Go digest": request, "Rust digest": spoken}
	for range 3 {
		if queued, err := store.enqueueDueRoutine(ctx, time.Now().Add(25*time.Hour)); err != nil || !queued {
			t.Fatalf("queue routine: %v, %v", queued, err)
		}
		job, err := store.claimLane(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		turn, err := store.turn(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		if request, ok := want[turn.Title]; ok {
			if !turn.canSearchWeb() || !slices.Equal(turn.publicResearch.Requests, []researchRequest{{User: request}}) {
				t.Fatalf("missing authenticated request: %+v", turn.publicResearch)
			}
			delete(want, turn.Title)
		} else if turn.canSearchWeb() || !turn.routineResearchMissing {
			t.Fatalf("unattributed prompt enabled research: %+v", turn.publicResearch)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing routine runs: %v", want)
	}
}

func TestResumedWorkAppendsQuestionBoundPublicResearch(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	const request = "Research public release notes. Ask which language first."
	task := startChatRequest(t, store, request)
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", "", &TurnAction{
		Handoff: &Handoff{Title: "Release notes", Brief: "Research public release notes.", ReadOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	if err := publishCodeRun(func(stream, data string) error { return addEvent(ctx, store.db, job.ID, stream, data) }, ChatCodeRun{
		RunID: "run_before_question", WorkCommand: true, Status: Succeeded, StartedAt: finished, FinishedAt: &finished,
		chatCodeResult: chatCodeResult{Command: "fmt", Stdout: "previous output"},
	}); err != nil {
		t.Fatal(err)
	}
	const question = "Which language has public release notes you want me to read?"
	if err := store.finish(ctx, job.ID, NeedsInput, "Waiting.", question); err != nil {
		t.Fatal(err)
	}
	detail, err := store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || detail.InputID == "" {
		t.Fatalf("work question: %+v, %v", detail, err)
	}
	if resumed, err := store.AnswerWorkInput(ctx, DefaultConversationID, detail.InputID, "Rust", "research-answer", nil); err != nil || !resumed {
		t.Fatalf("resume: %v, %v", resumed, err)
	}
	job, err = store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	got := turn.publicResearch.Requests
	if !turn.canSearchWeb() || len(got) != 2 || got[0].User != request || got[1].Choice == nil ||
		*got[1].Choice != (authorizationChoice{Question: question, Selected: "Rust"}) {
		t.Fatalf("research request: %+v", got)
	}
	detail, err = store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || detail.CodeRun != nil || len(detail.CodeRuns) != 1 || detail.CodeRuns[0].RunID != "run_before_question" {
		t.Fatalf("previous attempt was lost or shown as live: %+v, %v", detail, err)
	}
	run, err := store.ChatCodeOutput(ctx, DefaultConversationID, job.ID+"/run_before_question")
	if err != nil || run.Status != Succeeded || run.Stdout != "previous output" {
		t.Fatalf("previous output: %+v, %v", run, err)
	}
	const delegation = `{"request_id":"selected-notes","title":"Find release notes","brief":"Search sources for the selected language","read_only":true}`
	result, err := store.controlWork(ctx, job.ID, "delegate", json.RawMessage(delegation))
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.claimLane(ctx, true)
	if err != nil || child.ID != result.([]AssignedWork)[0].ID {
		t.Fatalf("delegation: %+v, %v", child, err)
	}
	childTurn, err := store.turn(ctx, child)
	if err != nil || len(childTurn.publicResearch.Requests) != 2 || childTurn.publicResearch.Requests[1].Choice == nil ||
		*childTurn.publicResearch.Requests[1].Choice != *got[1].Choice {
		t.Fatalf("delegated research lost the answer: %+v, %v", childTurn.publicResearch, err)
	}
	second := startChatRequest(t, store, "Research release notes for the second project. Ask which language first.")
	if err := store.finishTurn(ctx, second.ID, Succeeded, "Replied.", "", &TurnAction{
		Handoff: &Handoff{Title: "Second project", Brief: "Research the second project's public release notes.", ReadOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	other, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, other.ID, NeedsInput, "Waiting.", question); err != nil {
		t.Fatal(err)
	}
	detail, err = store.WorkDetails(ctx, DefaultConversationID, other.ID)
	if err != nil || detail.InputID == "" {
		t.Fatalf("second work question: %+v, %v", detail, err)
	}
	if resumed, err := store.AnswerWorkInput(ctx, DefaultConversationID, detail.InputID, "Go", "second-research-answer", nil); err != nil || !resumed {
		t.Fatalf("resume second job: %v, %v", resumed, err)
	}
	other, err = store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err = store.controlWork(ctx, other.ID, "delegate", json.RawMessage(delegation))
	if err != nil {
		t.Fatal(err)
	}
	if result.([]AssignedWork)[0].ID == child.ID {
		t.Fatal("identical briefs from different parents reused the Rust lookup")
	}
	secondChild, err := store.claimLane(ctx, true)
	if err != nil || secondChild.ID != result.([]AssignedWork)[0].ID {
		t.Fatalf("second delegation: %+v, %v", secondChild, err)
	}
	secondTurn, err := store.turn(ctx, secondChild)
	var choice *authorizationChoice
	if n := len(secondTurn.publicResearch.Requests); n != 0 {
		choice = secondTurn.publicResearch.Requests[n-1].Choice
	}
	if err != nil || choice == nil || choice.Selected != "Go" {
		t.Fatalf("second child got the wrong research scope: %+v, %v", secondTurn.publicResearch, err)
	}
}
