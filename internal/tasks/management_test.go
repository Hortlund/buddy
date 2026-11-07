package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestManagementChoiceStopsWaitingJobAndDeletesRoutine(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routine, err := store.AddRoutine(ctx, DefaultConversationID, Routine{Name: "ff", Prompt: "Ask for input.", Minutes: 1440})
	if err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueueDueRoutine(ctx, routine.NextAt); err != nil || !queued {
		t.Fatalf("schedule fixture: %v, %v", queued, err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, job.ID, NeedsInput, "Waiting for a reply.", "What should I do?"); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(store, nil, func(ctx context.Context, turn Turn, _ Emit) (string, error) {
		if turn.Messages[len(turn.Messages)-1].Text != "I don't know what ff is. Remove it." {
			return "", errors.New("a management card answer must not run the model")
		}
		stop := ManagementAction{Operation: "stop_job", Target: job.ID}
		remove := ManagementAction{Operation: "delete_routine", Target: routine.ID}
		response, err := json.Marshal(map[string]any{"reply": "", "management_choices": [][]ManagementAction{{remove, stop}, {remove}, {stop}}})
		if err != nil {
			return "", err
		}
		turn.coordinate = true
		return turn.decodeReply(string(response), nil)
	})
	running := make(chan struct{})
	go func() { defer close(running); worker.Run(ctx) }()
	defer func() { cancel(); <-running }()
	request, _, err := store.Send(ctx, DefaultConversationID, "I don't know what ff is. Remove it.", "remove-ff")
	if err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	waitForConversation(t, store, DefaultConversationID, func(c Conversation) bool {
		return slices.ContainsFunc(c.Messages, func(m Message) bool {
			return m.TaskID == request.ID && m.Card != nil && m.Card.Options[0] == "Both" &&
				strings.Contains(m.Card.OptionDetails[0], "Delete routine") && strings.Contains(m.Card.OptionDetails[0], "Stop job")
		})
	})
	before, err := store.Get(ctx, job.ID)
	if err != nil || before.Status != NeedsInput {
		t.Fatalf("card changed the job before the user clicked: %+v, %v", before, err)
	}
	answer, created, err := store.AnswerCard(ctx, DefaultConversationID, request.ID, 0)
	if err != nil || !created {
		t.Fatalf("couldn't answer the management card: %+v, %v", answer, err)
	}
	worker.Wake()
	waitForConversation(t, store, DefaultConversationID, func(c Conversation) bool {
		return len(c.Routines) == 0 && slices.ContainsFunc(c.Messages, func(m Message) bool {
			return m.TaskID == answer.ID && strings.Contains(m.Text, "Deleted the routine") && strings.Contains(m.Text, "Stopped the job")
		})
	})
	after, err := store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || after.Status != Cancelled || after.InputID != "" {
		t.Fatalf("the waiting job wasn't closed: %+v, %v", after, err)
	}
	retry, created, err := store.AnswerCard(ctx, DefaultConversationID, request.ID, 0)
	if err != nil || created || retry.ID != answer.ID {
		t.Fatalf("retry did not reuse the original answer: %+v, %v", retry, err)
	}
}

func TestManagementCancelsLocalRunAndScopesTargets(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bud, err := store.CreateAgent(ctx, Agent{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	privateRoutine, err := store.AddRoutine(ctx, bud.ConversationID, Routine{Name: "Private", Prompt: "Work.", Minutes: 1440})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.QueueVoiceWork(ctx, DefaultConversationID, DefaultAgentID, "call", "job", "Build", "Build the project.")
	if err != nil {
		t.Fatal(err)
	}
	started, stopped := make(chan struct{}), make(chan struct{})
	worker := NewWorker(store, nil, func(ctx context.Context, turn Turn, _ Emit) (string, error) {
		if turn.Origin == "handoff" {
			close(started)
			<-ctx.Done()
			close(stopped)
			return "", ctx.Err()
		}
		if turn.Messages[len(turn.Messages)-1].Text == "Delete routine Private." {
			turn.action.Manage = []ManagementAction{{Operation: "delete_routine", Target: privateRoutine.ID}}
		} else {
			turn.action.Manage = []ManagementAction{{Operation: "stop_job", Target: job.ID}}
		}
		return "", nil
	})
	running := make(chan struct{})
	go func() { defer close(running); worker.Run(ctx) }()
	defer func() { cancel(); <-running }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("job never started")
	}
	for i, message := range []string{"Delete routine Private.", "Stop the Build job."} {
		request, _, err := store.Send(ctx, DefaultConversationID, message, message)
		if err != nil {
			t.Fatal(err)
		}
		worker.Wake()
		expected := []string{"allowed scope", "is stopping"}[i]
		waitForConversation(t, store, DefaultConversationID, func(c Conversation) bool {
			return slices.ContainsFunc(c.Messages, func(m Message) bool {
				return m.TaskID == request.ID && m.Role == "assistant" && strings.Contains(m.Text, expected)
			})
		})
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("the local job never received cancellation")
	}
	other, err := store.Conversation(ctx, bud.ConversationID, 0)
	if err != nil || len(other.Routines) != 1 || other.Routines[0].ID != privateRoutine.ID {
		t.Fatalf("the out-of-scope routine changed: %+v, %v", other.Routines, err)
	}
}

func TestVoiceManagementUsesCallerWordsOrOffersAChatCard(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routine, err := store.AddRoutine(ctx, DefaultConversationID, Routine{Name: "Daily", Prompt: "Work.", Minutes: 1440})
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(store, nil, nil)
	plan := []ManagementAction{{Operation: "pause_routine", Target: routine.ID}}
	note, err := worker.ManageVoice(ctx, DefaultConversationID, DefaultAgentID, "call", "What happens if I pause the Daily routine?", plan, nil)
	if err != nil || !strings.Contains(note, "Choose an option") {
		t.Fatalf("ambiguous voice request did not offer a card: %s, %v", note, err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Routines) != 1 || !chat.Routines[0].Enabled ||
		!slices.ContainsFunc(chat.Messages, func(m Message) bool { return m.Card != nil }) {
		t.Fatalf("voice request changed state or failed to save the card: %+v, %v", chat, err)
	}
	note, err = worker.ManageVoice(ctx, DefaultConversationID, DefaultAgentID, "call", "Pause the Daily routine.", plan, nil)
	if err != nil || !strings.Contains(note, "Paused") {
		t.Fatalf("explicit voice request was not applied: %s, %v", note, err)
	}
	chat, err = store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Routines) != 1 || chat.Routines[0].Enabled {
		t.Fatalf("voice request did not pause the routine: %+v, %v", chat.Routines, err)
	}
}

func TestManagementBatchDoesNotApplyPartialChanges(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chat = DefaultConversationID
	keep, err := store.AddRoutine(ctx, chat, Routine{Name: "Keep", Prompt: "Do work.", Minutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	offline, err := store.AddRoutine(ctx, chat, Routine{Name: "Studio routine", Prompt: "Do work on Studio.", Minutes: 60, DeviceID: device.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	request, _, err := store.Send(ctx, chat, "Delete Keep and resume the Studio routine.", "batch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	plan := []ManagementAction{{Operation: "delete_routine", Target: keep.ID}, {Operation: "resume_routine", Target: offline.ID}}
	if err := store.finishTurn(ctx, request.ID, Succeeded, "Replied.", "", &TurnAction{Manage: plan}); err != nil {
		t.Fatal(err)
	}
	answer, _, err := store.AnswerCard(ctx, chat, request.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	saved, _, confirmed, err := store.confirmedManagement(ctx, answer.ID)
	if err != nil || !confirmed {
		t.Fatalf("missing saved plan: %+v, %v", saved, err)
	}
	if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", &TurnAction{Manage: saved, managementConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	conversation, err := store.Conversation(ctx, chat, 0)
	if err != nil || len(conversation.Routines) != 2 ||
		!slices.ContainsFunc(conversation.Messages, func(m Message) bool {
			return m.TaskID == answer.ID && strings.Contains(m.Text, "device was disconnected")
		}) {
		t.Fatalf("the batch partially succeeded or hid the failure: %+v, %v", conversation, err)
	}
}

func TestChatMemorySavesAndUpdatesWithoutCard(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetBudLearning(ctx, DefaultAgentID, false); err != nil {
		t.Fatal(err)
	}
	const request = "ok remember to call me bro"
	remember := ManagementAction{Operation: "remember", Text: "Call the user bro.", Evidence: "remember to call me bro"}
	for range 2 {
		turn := runManagementTestTurn(t, store, request, remember)
		if turn.Card != nil || !strings.Contains(turn.Text, "remember that") {
			t.Fatalf("remember prompted for confirmation: %+v", turn)
		}
	}
	notes, err := store.StandingMemories(ctx, DefaultAgentID)
	if err != nil || len(notes) != 1 || notes[0].Note != remember.Text {
		t.Fatalf("expected one saved note, got %+v, %v", notes, err)
	}
	correction := ManagementAction{Operation: "update_memory", Target: notes[0].ID,
		Text: "Call the user pal.", Evidence: "update my nickname to pal"}
	turn := runManagementTestTurn(t, store, "Please update my nickname to pal.", correction)
	notes, err = store.StandingMemories(ctx, DefaultAgentID)
	if err != nil || turn.Card != nil || len(notes) != 1 || notes[0].ID != correction.Target || notes[0].Note != correction.Text {
		t.Fatalf("correction did not update the original note: %+v, %+v, %v", turn, notes, err)
	}
	for _, unrelated := range []string{"What did you save?", "> remember to call me bro"} {
		turn = runManagementTestTurn(t, store, unrelated, remember)
		notes, err = store.StandingMemories(ctx, DefaultAgentID)
		if err != nil || turn.Card != nil || len(notes) != 1 || notes[0].Note != correction.Text {
			t.Fatalf("unverified evidence changed memory: %+v, %+v, %v", turn, notes, err)
		}
	}
}

func TestNamedRoutineDeletionRequiresConfirmation(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chat = DefaultConversationID
	routine, err := store.AddRoutine(ctx, chat, Routine{Name: "Daily", Prompt: "Review work.", Minutes: 1440})
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range []int{1, 0} {
		msg := runManagementTestTurn(t, store, "Delete the routine Daily.", ManagementAction{Operation: "delete_routine", Target: routine.ID})
		if msg.Card == nil || msg.Card.Confirmation != "destructive" || msg.Card.Question != "Delete routine “Daily”?" ||
			!slices.Equal(msg.Card.Options, []string{"Delete routine", "Cancel"}) {
			t.Fatalf("unexpected deletion confirmation: %+v", msg.Card)
		}
		before, err := store.Conversation(ctx, chat, 0)
		if err != nil || len(before.Routines) != 1 {
			t.Fatalf("the routine changed before confirmation: %+v, %v", before.Routines, err)
		}
		answer, _, err := store.AnswerCard(ctx, chat, msg.TaskID, choice)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.claim(ctx); err != nil {
			t.Fatal(err)
		}
		plan, _, confirmed, err := store.confirmedManagement(ctx, answer.ID)
		if err != nil || !confirmed {
			t.Fatalf("missing confirmed plan: %v, %v, %v", plan, confirmed, err)
		}
		if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", &TurnAction{Manage: plan, managementConfirmed: true}); err != nil {
			t.Fatal(err)
		}
		after, err := store.Conversation(ctx, chat, 0)
		if err != nil || len(after.Routines) != choice ||
			slices.ContainsFunc(after.Messages, func(m Message) bool { return m.TaskID == answer.ID && m.Role == "user" }) {
			t.Fatalf("wrong result or visible synthetic answer after choice %d: %+v, %v", choice, after, err)
		}
	}
}

func TestModelCanPauseRoutineWithoutMatchingACommandPhrase(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routine, err := store.AddRoutine(ctx, DefaultConversationID, Routine{Name: "Daily", Prompt: "Review work.", Minutes: 1440})
	if err != nil {
		t.Fatal(err)
	}
	const request = "Could we put Daily on hold for a bit?"
	reply := runManagementTestTurn(t, store, request, ManagementAction{Operation: "pause_routine",
		Target: routine.ID, Evidence: request})
	conversation, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || reply.Card != nil || len(conversation.Routines) != 1 || conversation.Routines[0].Enabled {
		t.Fatalf("model-selected pause did not apply: %+v, %+v, %v", reply, conversation.Routines, err)
	}
}

func TestLearnedMemoryCorrectionInvalidatesOlderForgetConfirmation(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const topic = "editor"
	const preference = "I use Vim for editing."
	learn, _, err := store.Send(ctx, DefaultConversationID, preference, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, learn.ID, Succeeded, "Replied.", "Got it.", &TurnAction{
		Learning: []LearnedNote{{Topic: topic, Note: preference, Evidence: preference}},
	}); err != nil {
		t.Fatal(err)
	}
	forget := runManagementTestTurn(t, store, "Forget which editor I use.", ManagementAction{Operation: "forget_learned_memory", Target: topic})
	if forget.Card == nil || forget.Card.Confirmation != "destructive" || !strings.Contains(forget.Card.Question, "Vim") {
		t.Fatalf("learned memory did not require confirmation: %+v", forget)
	}
	const correction = "Update my preferred editor to Zed."
	change := runManagementTestTurn(t, store, correction, ManagementAction{Operation: "update_learned_memory",
		Target: topic, Text: "Uses Zed for editing.", Evidence: correction})
	if change.Card != nil || !strings.Contains(change.Text, "Updated") {
		t.Fatalf("correction did not apply directly: %+v", change)
	}
	answer, _, err := store.AnswerCard(ctx, DefaultConversationID, forget.TaskID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	plan, _, confirmed, err := store.confirmedManagement(ctx, answer.ID)
	if err != nil || !confirmed {
		t.Fatalf("missing confirmed plan: %v, %v, %v", plan, confirmed, err)
	}
	if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", &TurnAction{Manage: plan, managementConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	docs, err := store.BudDocuments(ctx, DefaultAgentID)
	if err != nil || !strings.Contains(docs.Memory, "Zed") || strings.Contains(docs.Memory, "Vim") {
		t.Fatalf("stale confirmation removed a corrected note: %+v, %v", docs, err)
	}
}

func runManagementTestTurn(t *testing.T, store *Store, request string, operation ManagementAction) Message {
	t.Helper()
	ctx := t.Context()
	turn, _, err := store.Send(ctx, DefaultConversationID, request, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "", &TurnAction{Manage: []ManagementAction{operation}}); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range chat.Messages {
		if msg.TaskID == turn.ID && msg.Role == "assistant" {
			return msg
		}
	}
	t.Fatal("management turn produced no reply")
	return Message{}
}
