package tasks

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHandoffUsesTeammateWorkspaceWhileCompanionKeepsChatting(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, root); err != nil {
		t.Fatal(err)
	}
	buddy, err := store.Agent(ctx, DefaultAgentID)
	if err != nil || !buddy.AllowEdits {
		t.Fatalf("default workspace: %+v, %v", buddy, err)
	}
	coder, err := store.CreateAgent(ctx, Agent{Name: "Forge", Role: "Developer"})
	if err != nil || !coder.AllowEdits || coder.Workspace == buddy.Workspace {
		t.Fatalf("new agent workspace: %+v, %v", coder, err)
	}
	team, err := store.CreateGroup(ctx, GroupRequest{Name: "Workshop", Members: []string{buddy.ID, coder.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SendTo(ctx, DefaultConversationID, coder.ID, "hi", "private"); err == nil {
		t.Fatal("direct chat accepted an outside agent")
	}
	background := make(chan Turn, 1)
	release := make(chan struct{})
	const acknowledgement = "I’ll have Forge write the file and let you know how it goes."
	worker := NewWorker(store, nil, func(ctx context.Context, turn Turn, emit Emit) (string, error) {
		if turn.Origin == "handoff" {
			for _, update := range []struct{ stream, text string }{
				{"commentary", "Tests passed."},
				{"activity", "Making changes…"},
				{"commentary", "Tests passed. I reran them after the fix."},
				{"work_update", "Checking again."},
				{"work_update", "Checking again. The build is clean."},
			} {
				if err := emit(update.stream, update.text); err != nil {
					return "", err
				}
			}
			for _, text := range []string{"**Checking**", "**Checking** the workspace.", "Wrote result", "Wrote result.txt."} {
				if err := emit("commentary", text); err != nil {
					return "", err
				}
			}
			background <- turn
			select {
			case <-release:
				err := os.WriteFile(filepath.Join(turn.Agent.Workspace, "result.txt"), []byte("done"), 0600)
				return "Wrote result.txt.", err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if turn.Messages[len(turn.Messages)-1].Text == "make a file" {
			turn.action.Handoff = &Handoff{AgentID: coder.ID, Title: "Create result.txt", Brief: "Write the word done."}
			return acknowledgement, nil
		}
		return "Still here.", nil
	})
	lifetime, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(lifetime) }()
	defer func() { stop(); <-done }()

	parent, _, err := store.Send(ctx, team.ID, "make a file", "handoff")
	if err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	select {
	case got := <-background:
		if got.Agent.ID != coder.ID || got.Agent.Role != "Developer" || got.Agent.Workspace != coder.Workspace ||
			got.Messages[len(got.Messages)-1].TaskID != parent.ID || len(got.Team) != 2 {
			t.Fatalf("worker got the wrong assignment: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handoff didn't start")
	}
	followUp, _, err := store.SendTo(ctx, team.ID, coder.ID, "still there?", "follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Send(ctx, team.ID, "still there?", "follow-up"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same request key accepted a different recipient: %v", err)
	}
	worker.Wake()
	chat := waitForConversation(t, store, team.ID, func(chat Conversation) bool {
		for _, message := range chat.Messages {
			if message.TaskID == followUp.ID && message.Role == "assistant" {
				return message.AgentID == coder.ID && message.Text == "Still here."
			}
		}
		return false
	})
	if len(chat.Work) != 1 || chat.Work[0].Status != Running || chat.Active != nil {
		t.Fatalf("background work blocked or occupied chat: %+v", chat)
	}
	if !slices.ContainsFunc(chat.Messages, func(message Message) bool {
		return message.TaskID == parent.ID && message.Role == "assistant" && message.Text == acknowledgement
	}) {
		t.Fatalf("the chat did not use the Bud's acknowledgement: %+v", chat.Messages)
	}
	job := chat.Work[0]
	if _, _, err := store.SendWithWork(ctx, DefaultConversationID, "", "What's the status?", "cross-chat", nil, job.ID); err == nil {
		t.Fatal("a chat accepted a job from a different conversation")
	}
	question, created, err := store.SendWithWork(ctx, team.ID, "", "What's the status?", "job-question", nil, job.ID)
	if err != nil || !created {
		t.Fatalf("sending job question: %v", err)
	}
	if retry, created, err := store.SendWithWork(ctx, team.ID, "", "What's the status?", "job-question", nil, job.ID); err != nil || created || retry.ID != question.ID {
		t.Fatalf("retrying job question: %v, %v, %v", retry.ID, created, err)
	}
	if _, _, err := store.Send(ctx, team.ID, "What's the status?", "job-question"); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry changed the referenced job: %v", err)
	}
	turn, err := store.turn(ctx, question)
	if err != nil || turn.WorkDetails == nil || turn.WorkDetails.ID != job.ID ||
		turn.WorkDetails.Brief != "Write the word done." || turn.WorkDetails.Status != Running {
		t.Fatalf("job context in chat turn: %+v, %v", turn.WorkDetails, err)
	}
	close(release)
	chat = waitForConversation(t, store, team.ID, func(chat Conversation) bool {
		return len(chat.Work) == 1 && chat.Work[0].Status == Succeeded
	})
	i := slices.IndexFunc(chat.Messages, func(message Message) bool { return message.TaskID == job.ID && message.Role == "assistant" })
	if i < 0 || chat.Messages[i].Origin != "handoff" || chat.Messages[i].AgentName != coder.Name || chat.Messages[i].Text != "Wrote result.txt." {
		t.Fatalf("background result not posted to the originating group: %+v", chat.Messages)
	}
	details, err := store.WorkDetails(ctx, team.ID, job.ID)
	var notes []string
	for _, event := range details.Events {
		if event.Stream == "commentary" {
			notes = append(notes, event.Text)
		}
	}
	if err != nil || details.Result != "Wrote result.txt." || !slices.Equal(notes, []string{
		"Tests passed.", "Tests passed. I reran them after the fix.", "Checking again.",
		"Checking again. The build is clean.", "**Checking** the workspace.",
	}) {
		t.Fatalf("work history still includes answer or draft fragments: %+v, %v", details, err)
	}
	last := chat.Messages[i]
	directory, err := store.Directory(ctx)
	if err != nil || !directory.Groups[0].HasNewResult {
		t.Fatalf("finished handoff did not mark the group unread: %+v, %v", directory, err)
	}
	if err := store.MarkResultsRead(ctx, team.ID, last.ID); err != nil {
		t.Fatal(err)
	}
	directory, err = store.Directory(ctx)
	if err != nil || directory.Groups[0].HasNewResult {
		t.Fatalf("opening the group did not clear the result: %+v, %v", directory, err)
	}
	files, err := store.Workspace(ctx, coder.ID)
	if err != nil || len(files.Entries) != 1 || files.Entries[0].Name != "result.txt" {
		t.Fatalf("workspace files = %+v, %v", files, err)
	}
}

func TestWorkHistoryLoadsOlderFinishedJobsInOneChat(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	peer, err := store.CreateAgent(ctx, Agent{Name: "Peer"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 55 {
		job, err := store.QueueVoiceWork(ctx, DefaultConversationID, DefaultAgentID, "call", rand.Text(), "Review", "Review the report.")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
		if _, err := store.db.ExecContext(ctx, "UPDATE tasks SET status='succeeded' WHERE id=?", job.ID); err != nil {
			t.Fatal(err)
		}
	}
	other, err := store.QueueVoiceWork(ctx, peer.ConversationID, peer.ID, "call", "other", "Elsewhere", "Review a different report.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE tasks SET status='succeeded' WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueVoiceWork(ctx, DefaultConversationID, DefaultAgentID, "call", "open", "Still queued", "Review a new report."); err != nil {
		t.Fatal(err)
	}
	first, err := store.WorkHistory(ctx, DefaultConversationID, "")
	if err != nil || len(first.Items) != 50 || first.Items[0].ID != ids[54] || first.Next != ids[5] {
		t.Fatalf("first page has wrong bounds: count %d, cursor %q, error %v", len(first.Items), first.Next, err)
	}
	last, err := store.WorkHistory(ctx, DefaultConversationID, first.Next)
	if err != nil || len(last.Items) != 5 || last.Items[0].ID != ids[4] || last.Items[4].ID != ids[0] || last.Next != "" {
		t.Fatalf("last page has wrong bounds: count %d, cursor %q, error %v", len(last.Items), last.Next, err)
	}
}
func TestRoutineCoalescesMissedIntervalsAndNeverOverlaps(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routine, err := store.AddRoutine(ctx, DefaultConversationID, Routine{Name: "Review files", Prompt: "List recent files", Minutes: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := routine.NextAt.Add(10 * time.Minute)
	if queued, err := store.enqueueDueRoutine(ctx, now); err != nil || !queued {
		t.Fatalf("enqueue missed occurrence = %v, %v", queued, err)
	}
	if queued, err := store.enqueueDueRoutine(ctx, now.Add(time.Hour)); err != nil || queued {
		t.Fatalf("overlapping occurrence = %v, %v", queued, err)
	}
	if _, err := store.claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("routine was picked up by the chat lane: %v", err)
	}
	work, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, work.ID, Succeeded, "Finished.", "No new files."); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Routines) != 1 || len(chat.Work) != 1 || len(chat.Messages) != 1 {
		t.Fatalf("routine state = %+v, %v", chat, err)
	}
	if !chat.Routines[0].NextAt.Equal(now.Add(time.Minute)) || chat.Messages[0].Origin != "routine" {
		t.Fatalf("routine did not advance and post to chat: %+v", chat)
	}
	paused := false
	if err := store.UpdateRoutine(ctx, DefaultConversationID, routine.ID, &paused); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueueDueRoutine(ctx, now.Add(time.Hour)); err != nil || queued {
		t.Fatalf("paused occurrence = %v, %v", queued, err)
	}
}

func TestFailedHandoffDoesNotPromiseWork(t *testing.T) {
	store, err := OpenStore(t.Context(), filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent, _, err := store.Send(t.Context(), DefaultConversationID, "Make a file", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(t.Context()); err != nil {
		t.Fatal(err)
	}
	handoff := Handoff{AgentID: "agt_outside", Title: "Write a file", Brief: "Create notes.txt."}
	const promise = "I’ve handed it off and will let you know."
	if err := store.finishTurn(t.Context(), parent.ID, Succeeded, "Replied.", promise, &TurnAction{Handoff: &handoff}); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(t.Context(), DefaultConversationID, 0)
	if err != nil || len(chat.Work) != 0 || len(chat.Messages) != 2 ||
		chat.Messages[1].Text != "I couldn’t start that. Choose a Bud who belongs to this chat." {
		t.Fatalf("handoff result = %+v, %v", chat, err)
	}
}

func TestConflictingHandoffStillShowsChoice(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent, _, err := store.Send(ctx, DefaultConversationID, "Inspect the repository", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	const question = "Which repository should I inspect?"
	action := &TurnAction{
		Handoff: &Handoff{Title: "Inspect repository", Brief: "Inspect the API repository."},
		Card:    &ChoiceCard{Question: question, Options: []string{"API", "Desktop"}},
	}
	if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", "I’ve started inspecting the API.", action); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Work) != 0 || len(chat.Messages) != 2 ||
		chat.Messages[1].Text != "" || chat.Messages[1].Card == nil || chat.Messages[1].Card.Question != question {
		t.Fatalf("the question was lost or work was started: %+v, %v", chat, err)
	}
	if _, created, err := store.AnswerCard(ctx, DefaultConversationID, parent.ID, 0); err != nil || !created {
		t.Fatalf("the saved card could not be answered: %v, %v", created, err)
	}
}

func waitForConversation(t *testing.T, store *Store, id string, predicate func(Conversation) bool) Conversation {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		chat, err := store.Conversation(t.Context(), id, 0)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(chat) {
			return chat
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for conversation update")
	return Conversation{}
}

func TestChatRoutineAndConfirmationCommitTogether(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent, _, err := store.Send(ctx, DefaultConversationID, "Schedule an hourly workspace check", "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	var action TurnAction
	turn := Turn{coordinate: true, action: &action}
	reply, err := turn.decodeReply(`{"reply":"","handoff":null,"routine":{"agent_id":"","name":"Workspace check","prompt":"Check the workspace.","minutes":60}}`, nil)
	if err != nil || action.Routine == nil {
		t.Fatalf("decode routine: %+v, %v", action, err)
	}
	if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", reply, &action); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", reply, &action); !errors.Is(err, ErrInactive) {
		t.Fatalf("repeated completion: %v", err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Routines) != 1 || len(chat.Messages) != 2 || len(chat.Work) != 0 {
		t.Fatalf("unexpected saved turn: %+v, %v", chat, err)
	}
	if routine := chat.Routines[0]; routine.AgentID != DefaultAgentID || routine.Minutes != 60 || !routine.Enabled {
		t.Fatalf("unexpected routine: %+v", routine)
	}
	if chat.Messages[1].Text != "Routine scheduled · **Workspace check** · Hourly" {
		t.Fatalf("unexpected confirmation: %q", chat.Messages[1].Text)
	}
}

func TestChatRoutineRejectsAgentOutsideTheConversation(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent, _, err := store.Send(ctx, DefaultConversationID, "Create a daily routine", "routine")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	action := TurnAction{Routine: &RoutineDraft{AgentID: "agt_elsewhere", Name: "Daily check", Prompt: "Check files.", Minutes: 1440}}
	if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", "", &action); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Routines) != 0 || len(chat.Messages) != 2 ||
		!strings.Contains(chat.Messages[1].Text, "couldn't schedule") {
		t.Fatalf("invalid routine was accepted: %+v, %v", chat, err)
	}
}

func TestCardAnswerRoutesOnceToQuestionAuthor(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, _, err := store.Send(ctx, DefaultConversationID, "Ask me which device to use", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	action := &TurnAction{Card: &ChoiceCard{Question: "Which device?", Options: []string{"Server", "Studio"}}}
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", "", action); err != nil {
		t.Fatal(err)
	}
	answer, created, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, 1)
	if err != nil || !created {
		t.Fatalf("answer = %+v, %v, %v", answer, created, err)
	}
	replay, created, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, 1)
	if err != nil || created || replay.ID != answer.ID {
		t.Fatalf("replay = %+v, %v, %v", replay, created, err)
	}
	if _, _, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("second choice = %v", err)
	}
	if _, _, err := store.AnswerCard(ctx, "conv_elsewhere", task.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("different conversation = %v", err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 3 {
		t.Fatalf("conversation = %+v, %v", chat, err)
	}
	card := chat.Messages[1].Card
	if card == nil || card.Choice == nil || *card.Choice != 1 || chat.Messages[2].AnswerTo != "Which device?" ||
		chat.Messages[2].Text != "Studio" || chat.Messages[2].AgentID != DefaultAgentID {
		t.Fatalf("card and answer = %+v", chat.Messages)
	}
}

func TestRemoteJobCannotBeClaimedOrFinishedByAnotherDevice(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	one, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.EnrollDevice(ctx, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Send(ctx, DefaultConversationID, "Work on Studio", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	action := &TurnAction{Handoff: &Handoff{DeviceID: one.ID, Title: "Inspect project", Brief: "Read the project summary."}}
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", "", action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("server worker claimed the remote assignment: %v", err)
	}
	if job, err := store.ClaimDeviceJob(ctx, two.ID); err != nil || job != nil {
		t.Fatalf("other device got %+v, %v", job, err)
	}
	job, err := store.ClaimDeviceJob(ctx, one.ID)
	if err != nil || job == nil || job.Agent.Workspace != "" || job.Brief != "Read the project summary." {
		t.Fatalf("device got %+v, %v", job, err)
	}
	report := DeviceResult{Lease: job.Lease, Status: Succeeded, Reply: "I found the summary."}
	if err := store.FinishDeviceJob(ctx, two.ID, job.ID, report); !errors.Is(err, ErrInactive) {
		t.Fatalf("other device submitted a result: %v", err)
	}
	if _, err := store.cancel(ctx, job.ID, false); err != nil {
		t.Fatal(err)
	}
	status, err := store.PulseDeviceJob(ctx, one.ID, job.ID, DevicePulse{Lease: job.Lease})
	if err != nil || status != Cancelling {
		t.Fatalf("device could not see cancellation: %s, %v", status, err)
	}
	if err := store.FinishDeviceJob(ctx, one.ID, job.ID, report); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDeviceJob(ctx, one.ID, job.ID, report); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 3 || len(chat.Work) != 1 ||
		chat.Work[0].Device != "Studio" || chat.Work[0].Status != Cancelled {
		t.Fatalf("device result = %+v, %v", chat, err)
	}
	if err := store.RevokeDevice(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	if store.AuthenticateDevice(ctx, one.ID, one.Token) {
		t.Fatal("revoked credential remained valid")
	}
}

func TestOldCardAnswerPreservesOriginalRequest(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const original = "Create a report of the open PRs."
	first, _, err := store.Send(ctx, DefaultConversationID, original, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	card := &TurnAction{Card: &ChoiceCard{Question: "Which project?", Options: []string{"Buddy", "Tools"}}}
	if err := store.finishTurn(ctx, first.ID, Succeeded, "Replied.", "", card); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.AnswerCard(ctx, DefaultConversationID, first.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	card = &TurnAction{Card: &ChoiceCard{Question: "Which device?", Options: []string{"Server", "Studio"}}}
	if err := store.finishTurn(ctx, second.ID, Succeeded, "Replied.", "", card); err != nil {
		t.Fatal(err)
	}

	for range 24 {
		filler, _, err := store.Send(ctx, DefaultConversationID, strings.Repeat("x", 2000), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.claim(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.finish(ctx, filler.ID, Succeeded, "Replied.", strings.Repeat("y", 2000)); err != nil {
			t.Fatal(err)
		}
	}

	answer, _, err := store.AnswerCard(ctx, DefaultConversationID, second.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, answer)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{original, "Buddy", "Which project?", "Which device?"} {
		if !slices.ContainsFunc(turn.Messages, func(m Message) bool {
			return m.Text == expected || m.Card != nil && m.Card.Question == expected
		}) {
			t.Errorf("old card context omitted %q", expected)
		}
	}
	last := turn.Messages[len(turn.Messages)-1]
	if last.Text != "Studio" || last.AnswerTo != "Which device?" {
		t.Fatalf("latest answer = %+v", last)
	}
}

func TestChoiceCanBeRetriedAfterAnUnfinishedAnswer(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	plan, _, err := store.SendMessage(ctx, DefaultConversationID, "choice-retry", MessageRequest{Text: "Review the build.", Background: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	card := &ChoiceCard{Question: "Which checkout?", Options: []string{"Stable", "Beta"}}
	if err := store.finishTurn(ctx, plan.ID, Succeeded, "Replied.", "", &TurnAction{Card: card}); err != nil {
		t.Fatal(err)
	}
	var previous string
	for i, status := range []Status{Cancelled, Failed, Interrupted, Succeeded} {
		choice := i % len(card.Options)
		answer, created, err := store.AnswerCard(ctx, DefaultConversationID, plan.ID, choice)
		if err != nil || !created || answer.ID == previous {
			t.Fatalf("choice retry returned %+v, %v, %v", answer, created, err)
		}
		previous = answer.ID
		if duplicate, created, err := store.AnswerCard(ctx, DefaultConversationID, plan.ID, choice); err != nil || created || duplicate.ID != answer.ID {
			t.Fatalf("repeated click returned %+v, %v, %v", duplicate, created, err)
		}
		claimed, err := store.claimLane(ctx, false)
		if err != nil || claimed.ID != answer.ID {
			t.Fatalf("claim answer: %+v, %v", claimed, err)
		}
		turn, err := store.turn(ctx, claimed)
		if err != nil || turn.Origin != backgroundRequest || turn.Messages[len(turn.Messages)-1].AnswerTo != card.Question {
			t.Fatalf("lost context on retry: %+v, %v", turn, err)
		}
		if err := store.finish(ctx, answer.ID, status, "Finished.", ""); err != nil {
			t.Fatal(err)
		}
		chat, err := store.Conversation(ctx, DefaultConversationID, 0)
		if err != nil || !slices.ContainsFunc(append(chat.Messages, chat.EarlierUpdates...), func(m Message) bool {
			return m.TaskID == plan.ID && m.Card != nil && (m.Card.Choice != nil) == (status == Succeeded)
		}) {
			t.Fatalf("wrong choice state after %s: %v", status, err)
		}
		for _, message := range chat.Messages {
			if message.TaskID == answer.ID && message.Role == "user" && message.AnswerTo != card.Question {
				t.Fatalf("retry discarded the answer's link: %+v", message)
			}
		}
		if status == Interrupted {
			for range 61 {
				filler, _, err := store.Send(ctx, DefaultConversationID, "An unrelated message", "")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.claimLane(ctx, false); err != nil {
					t.Fatal(err)
				}
				if err := store.finish(ctx, filler.ID, Succeeded, "Finished.", "An unrelated reply"); err != nil {
					t.Fatal(err)
				}
			}
			latest, err := store.Conversation(ctx, DefaultConversationID, 0)
			if err != nil || !slices.ContainsFunc(latest.EarlierUpdates, func(m Message) bool {
				return m.TaskID == plan.ID && m.Card != nil && m.Card.Choice == nil
			}) {
				t.Fatalf("the reopened choice did not reach clients that loaded it earlier: %+v, %v", latest.EarlierUpdates, err)
			}
		}
	}
}

func TestDeviceLeaseRespectsDeadlineAndCancellation(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	device, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(title string) {
		t.Helper()
		parent, _, err := store.Send(ctx, DefaultConversationID, "Inspect the project", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.claim(ctx); err != nil {
			t.Fatal(err)
		}
		action := &TurnAction{Handoff: &Handoff{DeviceID: device.ID, Title: title, Brief: "List files."}}
		if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", "", action); err != nil {
			t.Fatal(err)
		}
	}
	enqueue("Inspect the first project")
	enqueue("Inspect the second project")
	job, err := store.ClaimDeviceJob(ctx, device.ID)
	if err != nil || job == nil {
		t.Fatalf("claim = %+v, %v", job, err)
	}
	deadline := time.Now().Add(8 * time.Second).UnixMicro()
	setDeadline := func(deadline int64) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, "UPDATE tasks SET started=?-timeout*1000000 WHERE id=?", deadline, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	expiry := func() int64 {
		t.Helper()
		var got int64
		if err := store.db.QueryRowContext(ctx, "SELECT expires FROM device_runs WHERE task_id=?", job.ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	setDeadline(deadline)
	pulse := DevicePulse{Lease: job.Lease}
	if status, err := store.PulseDeviceJob(ctx, device.ID, job.ID, pulse); err != nil || status != Running || expiry() != deadline {
		t.Fatalf("heartbeat = %q, %v, expiry = %d, deadline = %d", status, err, expiry(), deadline)
	}
	// Simulate a prior lease that extends past the absolute job deadline.
	setDeadline(time.Now().Add(-time.Second).UnixMicro())
	if _, err := store.PulseDeviceJob(ctx, device.ID, job.ID, pulse); !errors.Is(err, ErrInactive) {
		t.Fatalf("late heartbeat = %v", err)
	}
	report := DeviceResult{Lease: job.Lease, Status: Succeeded, Reply: "Done."}
	if err := store.FinishDeviceJob(ctx, device.ID, job.ID, report); !errors.Is(err, ErrInactive) {
		t.Fatalf("late result = %v", err)
	}
	if err := store.ExpireDeviceJobs(ctx); err != nil {
		t.Fatal(err)
	}
	firstID := job.ID
	job, err = store.ClaimDeviceJob(ctx, device.ID)
	if err != nil || job == nil || job.ID == firstID {
		t.Fatalf("claim after expiry = %+v, %v", job, err)
	}
	if _, err := store.cancel(ctx, job.ID, false); err != nil {
		t.Fatal(err)
	}
	before := expiry()
	if status, err := store.PulseDeviceJob(ctx, device.ID, job.ID, DevicePulse{Lease: job.Lease}); err != nil || status != Cancelling || expiry() != before {
		t.Fatalf("cancel heartbeat = %q, %v, expiry = %d, before = %d", status, err, expiry(), before)
	}
}

func TestRevokedRoutineDeviceIsReported(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	device, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	routine, err := store.AddRoutine(ctx, DefaultConversationID, Routine{
		DeviceID: device.ID, Name: "Daily check", Prompt: "List files.", Minutes: 1440,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Routines) != 1 || chat.Routines[0].Enabled || !chat.Routines[0].DeviceDisconnected {
		t.Fatalf("revoked routine = %+v, %v", chat.Routines, err)
	}
	enable := true
	var invalid *InputError
	err = store.UpdateRoutine(ctx, DefaultConversationID, routine.ID, &enable)
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "device was disconnected") {
		t.Fatalf("resume revoked routine = %v", err)
	}
	if err := store.UpdateRoutine(ctx, DefaultConversationID, routine.ID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreHoldsExternalWorkspaceUntilConfirmed(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent.Workspace, agent.AllowEdits = t.TempDir(), false
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	job, _, err := store.Send(ctx, DefaultConversationID, "Review the project", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRoutine(ctx, DefaultConversationID, Routine{Name: "Review", Prompt: "Review files", Minutes: 1}); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := store.PrepareRestoredHome(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unconfirmed job was claimable: %v", err)
	}
	if queued, err := store.enqueueDueRoutine(ctx, future); err != nil || queued {
		t.Fatalf("an unconfirmed routine could run: %v", err)
	}
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err == nil {
		t.Fatal("saving settings implicitly confirmed the old working folder")
	}
	agent.Workspace, agent.ConfirmWorkspace, agent.AllowEdits = t.TempDir(), true, true
	saved, err := store.SaveAgent(ctx, agent.ID, agent)
	if err != nil || saved.WorkspaceNeedsReview {
		t.Fatalf("working folder was not confirmed: %+v (%v)", saved, err)
	}
	claimed, err := store.claimLane(ctx, false)
	if err != nil || claimed.ID != job.ID || claimed.Repository != saved.Workspace {
		t.Fatalf("the held job did not resume in the new folder: %+v (%v)", claimed, err)
	}
	turn, err := store.turn(ctx, claimed)
	if err != nil || turn.Agent.AllowEdits {
		t.Fatalf("confirmation widened permissions of a queued read-only job: %v", err)
	}
	if queued, err := store.enqueueDueRoutine(ctx, future); err != nil || !queued {
		t.Fatalf("the confirmed routine was still held: %v", err)
	}
}

func TestBackgroundPlannerCanRouteRemoteWorkWithUnconfirmedWorkspace(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	agent, err := s.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent.Workspace = t.TempDir()
	if _, err := s.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(ctx, "Build Mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareRestoredHome(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, DefaultConversationID, "Check the old checkout", "local"); err == nil {
		t.Fatal("ordinary chat accepted the unconfirmed local folder")
	}
	for _, destination := range []string{device.ID, ""} {
		task, _, err := s.SendMessage(ctx, DefaultConversationID, "plan-"+destination, MessageRequest{
			Text: "Review the build.", Background: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := s.claimLane(ctx, false)
		if err != nil || claimed.ID != task.ID || claimed.Repository != "" {
			t.Fatalf("planner was tied to the old checkout: %+v, %v", claimed, err)
		}
		action := &TurnAction{Handoffs: []Handoff{{
			DeviceID: destination, Title: "Build review", Brief: "Review the build.",
		}}}
		if err := s.finishTurn(ctx, task.ID, Succeeded, "Replied.", "", action); err != nil {
			t.Fatal(err)
		}
	}
	job, err := s.ClaimDeviceJob(ctx, device.ID)
	if err != nil || job == nil || job.Title != "Build review" {
		t.Fatalf("worker did not receive the assignment: %+v, %v", job, err)
	}
	if _, err := s.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the unconfirmed local job was claimable: %v", err)
	}
}

func TestBackgroundPlannerKeepsTheSubmittedWorkspace(t *testing.T) {
	for _, mode := range []string{"direct", "reply", "choice", "restored"} {
		t.Run(mode, func(t *testing.T) {
			ctx, s := t.Context(), teamTestStore(t)
			const request = "Edit the release notes in your workspace."
			agent, err := s.Agent(ctx, DefaultAgentID)
			if err != nil {
				t.Fatal(err)
			}
			agent.Workspace = t.TempDir()
			agent, err = s.SaveAgent(ctx, agent.ID, agent)
			if err != nil {
				t.Fatal(err)
			}
			original := agent.Workspace
			plan, _, err := s.SendMessage(ctx, DefaultConversationID, "original", MessageRequest{Text: request, Background: true})
			if err != nil {
				t.Fatal(err)
			}
			claim := func(id string) {
				t.Helper()
				task, err := s.claimLane(ctx, false, original)
				if err != nil || task.ID != id || task.Repository != "" {
					t.Fatalf("planner should run independently of the workspace: %+v, %v", task, err)
				}
			}
			claim(plan.ID)
			if mode != "direct" {
				action := &TurnAction{backgroundQuestion: true}
				if mode == "choice" {
					action = &TurnAction{Card: &ChoiceCard{Question: "Which release?", Options: []string{"Beta", "Stable"}}}
				}
				if err := s.finishTurn(ctx, plan.ID, Succeeded, "Replied.", "Which release?", action); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "restored" {
				if err := s.PrepareRestoredHome(ctx); err != nil {
					t.Fatal(err)
				}
				agent.ConfirmWorkspace = true
			}
			agent.Workspace = t.TempDir()
			agent, err = s.SaveAgent(ctx, agent.ID, agent)
			if err != nil {
				t.Fatal(err)
			}
			action := &TurnAction{backgroundPlan: true, Handoffs: []Handoff{{Title: "Release notes", Brief: request}}}
			// An otherwise identical job already exists in the new checkout.
			if _, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, agent.ID, "call", "new-workspace", action.Handoffs); err != nil {
				t.Fatal(err)
			}
			var final = plan
			switch mode {
			case "reply", "restored":
				final, _, err = s.SendMessage(ctx, DefaultConversationID, "answer", MessageRequest{Text: "Beta", BackgroundAnswer: plan.ID})
			case "choice":
				final, _, err = s.AnswerCard(ctx, DefaultConversationID, plan.ID, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != "direct" {
				claim(final.ID)
			}
			if err := s.finishTurn(ctx, final.ID, Succeeded, "Replied.", "", action); err != nil {
				t.Fatal(err)
			}
			work, err := s.WorkOverview(ctx, DefaultConversationID)
			if mode == "restored" {
				updated, getErr := s.Get(ctx, final.ID)
				if err != nil || getErr != nil || len(work) != 1 || updated.Status != Failed {
					t.Fatalf("restored request used an unconfirmed checkout: %+v, %v, %v", work, err, getErr)
				}
				return
			}
			if err != nil || len(work) != 2 {
				t.Fatalf("expected separate jobs for distinct checkouts: %+v, %v", work, err)
			}
			job, err := s.claimLane(ctx, true, agent.Workspace)
			if err != nil || job.Repository != original {
				t.Fatalf("job lost its original working folder: %+v, %v", job, err)
			}
		})
	}
}

func TestBackgroundPlannerKeepsNamedBudsSubmittedWorkspace(t *testing.T) {
	for _, mode := range []string{"reply", "choice", "restored"} {
		t.Run(mode, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			forge, err := store.CreateAgent(ctx, Agent{Name: "Forge"})
			if err != nil {
				t.Fatal(err)
			}
			forge.Workspace, forge.AllowEdits = t.TempDir(), true
			forge, err = store.SaveAgent(ctx, forge.ID, forge)
			if err != nil {
				t.Fatal(err)
			}
			original := forge.Workspace
			chat := DefaultConversationID
			const brief = "Have Forge edit the release notes in its workspace."
			plan, _, err := store.SendMessage(ctx, chat, "forge-plan", MessageRequest{Text: brief, Background: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.claimLane(ctx, false); err != nil {
				t.Fatal(err)
			}
			question := &TurnAction{backgroundQuestion: true}
			if mode == "choice" {
				question = &TurnAction{Card: &ChoiceCard{Question: "Which release?", Options: []string{"Beta", "Stable"}}}
			}
			if err := store.finishTurn(ctx, plan.ID, Succeeded, "Replied.", "Which release?", question); err != nil {
				t.Fatal(err)
			}
			if mode == "restored" {
				if err := store.PrepareRestoredHome(ctx); err != nil {
					t.Fatal(err)
				}
				forge.ConfirmWorkspace = true
			}
			forge.Workspace = t.TempDir()
			if _, err := store.SaveAgent(ctx, forge.ID, forge); err != nil {
				t.Fatal(err)
			}
			var answer Task
			if mode == "choice" {
				answer, _, err = store.AnswerCard(ctx, chat, plan.ID, 0)
			} else {
				answer, _, err = store.SendMessage(ctx, chat, "forge-answer", MessageRequest{Text: "Beta", BackgroundAnswer: plan.ID})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.claimLane(ctx, false); err != nil {
				t.Fatal(err)
			}
			action := &TurnAction{backgroundPlan: true, Handoffs: []Handoff{{AgentID: forge.ID, Title: "Release notes", Brief: brief}}}
			if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", action); err != nil {
				t.Fatal(err)
			}
			job, err := store.claimLane(ctx, true)
			if mode == "restored" {
				finished, getErr := store.Get(ctx, answer.ID)
				if !errors.Is(err, ErrNotFound) || getErr != nil || finished.Status != Failed {
					t.Fatalf("restored plan used Forge's unconfirmed workspace: %+v, %v, %v", job, err, getErr)
				}
				return
			}
			if err != nil || job.Repository != original {
				t.Fatalf("job did not keep Forge's original workspace: %+v, %v", job, err)
			}
		})
	}
}

func TestArchivedGroupReopensBackgroundClarification(t *testing.T) {
	for _, choice := range []bool{false, true} {
		name := "typed answer"
		if choice {
			name = "choice card"
		}
		t.Run(name, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			forge, err := store.CreateAgent(ctx, Agent{Name: "Forge"})
			if err != nil {
				t.Fatal(err)
			}
			group, err := store.CreateGroup(ctx, GroupRequest{Name: "Review", Members: []string{DefaultAgentID, forge.ID}})
			if err != nil {
				t.Fatal(err)
			}
			plan, _, err := store.SendMessage(ctx, group.ID, "review-plan", MessageRequest{Text: "Review the logs.", Background: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.claimLane(ctx, false); err != nil {
				t.Fatal(err)
			}
			action := &TurnAction{backgroundQuestion: true, backgroundPlan: true}
			if choice {
				action = &TurnAction{Card: &ChoiceCard{Question: "Which build?", Options: []string{"Beta", "Stable"}}, backgroundPlan: true}
			}
			if err := store.finishTurn(ctx, plan.ID, Succeeded, "Replied.", "Which build?", action); err != nil {
				t.Fatal(err)
			}
			answer := func(key string) Task {
				t.Helper()
				var task Task
				var created bool
				var err error
				if choice {
					task, created, err = store.AnswerCard(ctx, group.ID, plan.ID, 0)
				} else {
					task, created, err = store.SendMessage(ctx, group.ID, key, MessageRequest{Text: "Beta", BackgroundAnswer: plan.ID})
				}
				if err != nil || !created {
					t.Fatalf("answer was rejected: %+v, %v", task, err)
				}
				if _, err := store.claimLane(ctx, false); err != nil {
					t.Fatal(err)
				}
				return task
			}
			first := answer("first")
			if err := store.SetGroupArchived(ctx, group.ID, true); err != nil {
				t.Fatal(err)
			}
			finish := func(id string) {
				t.Helper()
				action := &TurnAction{backgroundPlan: true, Handoffs: []Handoff{{Title: "Review logs", Brief: "Review the Beta build logs."}}}
				if err := store.finishTurn(ctx, id, Succeeded, "Replied.", "", action); err != nil {
					t.Fatal(err)
				}
			}
			finish(first.ID)
			if task, err := store.Get(ctx, first.ID); err != nil || task.Status != Failed {
				t.Fatalf("archived turn was accepted: %+v, %v", task, err)
			}
			if _, err := store.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a job was queued while archived: %v", err)
			}
			if err := store.SetGroupArchived(ctx, group.ID, false); err != nil {
				t.Fatal(err)
			}
			retry := answer("retry")
			finish(retry.ID)
			if job, err := store.claimLane(ctx, true); err != nil || job.Kind != BackgroundWork {
				t.Fatalf("retry did not queue the job: %+v, %v", job, err)
			}
		})
	}
}
