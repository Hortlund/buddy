package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buddy/internal/tasks"
)

func TestVoiceAnswerRequiresExactPendingQuestion(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 6*time.Second)
	defer stop()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chat, err := store.Conversation(ctx, tasks.DefaultConversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	const ordinaryRequest = "For the next release, have Forge review the changelog in the background."
	roomWithoutJobs := newVoiceRoom(ctx, nil, store, nil, chat, "", nil)
	roomWithoutJobs.requested = true // Keep the routed reply available for inspection.
	if err := roomWithoutJobs.routeUser(&roomMember{agent: chat.Agent}, ordinaryRequest); err != nil {
		t.Fatal(err)
	}
	if roomWithoutJobs.reply.tools != voiceCaller || roomWithoutJobs.workAnswer.input != "" {
		t.Fatalf("ordinary request lost its tools: %+v", roomWithoutJobs.reply)
	}
	jobs, err := store.QueueVoiceAssignments(ctx, chat.ID, chat.Agent.ID, "call", "questions", []tasks.Handoff{
		{Title: "Release review", Brief: "Review the release."},
		{Title: "Deploy check", Brief: "Check the deployment."},
		{Title: "Deploy check", Brief: "Check the other deployment."},
		{Title: "Granska Ändringar", Brief: "Review the changes."},
		{Title: "Release review staging", Brief: "Review the staging release."},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := tasks.NewWorker(store, nil, func(_ context.Context, turn tasks.Turn, _ tasks.Emit) (string, error) {
		if strings.Contains(strings.ToLower(turn.Brief), "use beta.") {
			return "Reviewed beta.", nil
		}
		if strings.Contains(turn.Brief, "Use stable.") {
			return "Which build should I review?", tasks.ErrWorkNeedsInput
		}
		return "Which branch should I use?", tasks.ErrWorkNeedsInput
	})
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(run) }()
	defer func() { cancel(); <-done }()
	var review, deploy, swedish tasks.WorkDetails
	waitFor(t, ctx, func() bool {
		review, _ = store.WorkDetails(ctx, chat.ID, jobs[0].ID)
		deploy, _ = store.WorkDetails(ctx, chat.ID, jobs[1].ID)
		duplicate, _ := store.WorkDetails(ctx, chat.ID, jobs[2].ID)
		swedish, _ = store.WorkDetails(ctx, chat.ID, jobs[3].ID)
		staging, _ := store.WorkDetails(ctx, chat.ID, jobs[4].ID)
		return review.Status == tasks.NeedsInput && deploy.Status == tasks.NeedsInput && duplicate.Status == tasks.NeedsInput && swedish.Status == tasks.NeedsInput && staging.Status == tasks.NeedsInput
	})
	dial, peers := newTestVoiceProvider(t)
	client, err := dial(ctx, "test-voice-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	<-peers
	room := newVoiceRoom(ctx, client, store, worker, chat, "test-voice-secret", dial)
	question := voiceWorkQuestion{review.ID, review.InputID, review.Title, review.Question}
	provider, err := dial(ctx, "test-voice-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.CloseNow()
	<-peers
	member := &roomMember{agent: chat.Agent, conn: provider}
	room.members, room.selected, room.listener = []*roomMember{member}, member, member
	room.tools = voiceCaller
	lookup, _, err := room.performTool(member, realtimeItem{Name: "buddy_work", Arguments: `{"job_id":"` + review.ID + `"}`})
	if err != nil || !strings.Contains(lookup, review.Question) || room.reply == nil || room.reply.question != question {
		t.Fatalf("lookup did not attach the exact pending question: %q, %+v, %v", lookup, room.reply, err)
	}
	if err := room.advance(); err != nil {
		t.Fatal(err)
	}
	room.response, room.caption = "ask-review", review.Question
	room.playback = &roomPlayback{item: "question-audio"}
	completed := realtimeEvent{Type: "response.done"}
	completed.Response.ID, completed.Response.Status = room.response, "completed"
	if err := room.handleRealtime(member, completed); err != nil {
		t.Fatal(err)
	}
	if room.spokenQuestion.input != "" {
		t.Fatal("a question was bound before the caller heard it")
	}
	if err := room.handleSpeech(member, realtimeEvent{Type: "input_audio_buffer.speech_started", Item: "early-answer"}); err != nil {
		t.Fatal(err)
	}
	if err := room.handleSpeech(member, realtimeEvent{
		Type: "conversation.item.input_audio_transcription.completed", Item: "early-answer", Transcript: "Use beta.",
	}); err != nil {
		t.Fatal(err)
	}
	if room.workAnswer.input != "" || room.deferredCallerWords != "Use beta." {
		t.Fatal("the answer was routed before playback was confirmed")
	}
	if err := room.input(roomInput{event: VoiceEvent{Type: "played", Item: "question-audio"}}); err != nil {
		t.Fatal(err)
	}
	if room.workAnswer.voiceWorkQuestion != question || room.workAnswer.text != "Use beta." {
		t.Fatalf("the playback receipt did not bind the caller's answer: %+v", room.workAnswer)
	}
	// A partial cut must never authorize an answer to an unheard question.
	room.clearReplyRequest()
	room.playback = &roomPlayback{item: "partial-question", bytes: 4800, ending: true, question: question}
	if err := room.handleSpeech(member, realtimeEvent{Type: "input_audio_buffer.speech_started", Item: "partial-answer"}); err != nil {
		t.Fatal(err)
	}
	if err := room.handleSpeech(member, realtimeEvent{
		Type: "conversation.item.input_audio_transcription.completed", Item: "partial-answer", Transcript: "Use beta.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := room.input(roomInput{event: VoiceEvent{Type: "cut", Item: "partial-question", Millis: 20}}); err != nil {
		t.Fatal(err)
	}
	if room.workAnswer.input != "" || room.spokenQuestion.input != "" {
		t.Fatal("the answer was bound to a question the caller did not hear")
	}
	room.clearReplyRequest()
	room.playback = &roomPlayback{item: "repeated-question", transcript: question.prompt, ending: true, question: question}
	if err := room.input(roomInput{event: VoiceEvent{Type: "played", Item: "repeated-question"}}); err != nil {
		t.Fatal(err)
	}
	room.playback = &roomPlayback{item: "unrelated-update", transcript: "Another job finished.", ending: true}
	if err := room.input(roomInput{event: VoiceEvent{Type: "played", Item: "unrelated-update"}}); err != nil {
		t.Fatal(err)
	}
	if question.wasRead("Release review is waiting for you.") {
		t.Fatal("status summary was treated as a spoken question")
	}
	if err := room.handleSpeech(member, realtimeEvent{Type: "input_audio_buffer.speech_started", Item: "noise"}); err != nil {
		t.Fatal(err)
	}
	if err := room.handleSpeech(member, realtimeEvent{Type: "conversation.item.input_audio_transcription.failed", Item: "noise"}); err != nil {
		t.Fatal(err)
	}
	if room.spokenQuestion != question || !room.spokenQuestionPending {
		t.Fatal("failed transcription discarded the question that was read aloud")
	}
	if err := room.handleSpeech(member, realtimeEvent{Type: "input_audio_buffer.speech_started", Item: "answer-audio"}); err != nil {
		t.Fatal(err)
	}
	if room.callerQuestion != question {
		t.Fatalf("spoken question wasn't bound to the next caller turn: %+v", room.callerQuestion)
	}
	if bound, _ := room.resolveWorkAnswer("Use beta."); bound.voiceWorkQuestion != question || bound.text != "Use beta." {
		t.Fatalf("the next utterance lost its question: %+v", bound)
	}
	if room.spokenQuestionPending {
		t.Fatal("an actual utterance did not consume the spoken question binding")
	}
	room.tools = voiceCaller
	room.directory = []tasks.AgentRef{{ID: chat.Agent.ID, Name: chat.Agent.Name}}
	answer := func(job, input, text string) string {
		t.Helper()
		args, _ := json.Marshal(object{"job_id": job, "input_id": input, "answer": text})
		result, _, err := room.performTool(nil, realtimeItem{Name: "answer_work", CallID: "answer", Arguments: string(args)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, text := range []string{"How is the beta release going?", "Buddy, what's the status", "For Release review, could you give me an update"} {
		room.callerQuestion = question
		bound, _ := room.resolveWorkAnswer(text)
		if bound.input != "" {
			t.Fatalf("inquiry was accepted as an answer: %q", text)
		}
	}
	room.workAnswer, _ = room.resolveWorkAnswer("Use beta.")
	if result := answer(review.ID, review.InputID, "Use beta."); !strings.Contains(result, "no matching spoken answer") {
		t.Fatal(result)
	}
	room.callerQuestion = question
	room.workAnswer, _ = room.resolveWorkAnswer("Use beta.")
	for _, input := range [][3]string{
		{deploy.ID, deploy.InputID, "Use beta."}, {review.ID, deploy.InputID, "Use beta."}, {review.ID, review.InputID, "beta"},
	} {
		if result := answer(input[0], input[1], input[2]); !strings.Contains(result, "no matching spoken answer") {
			t.Fatal(result)
		}
	}
	if named, _ := room.resolveWorkAnswer("For Release review, Use beta."); named.job != review.ID || named.input != review.InputID {
		t.Fatalf("wrong explicitly named question: %+v", named)
	}
	room.callerQuestion = voiceWorkQuestion{deploy.ID, deploy.InputID, deploy.Title, deploy.Question}
	if named, _ := room.resolveWorkAnswer("For Release Review use beta."); named.job != review.ID || named.input != review.InputID || named.text != "use beta." {
		t.Fatalf("an unpunctuated answer was not routed to the named job: %+v", named)
	}
	room.callerQuestion = question
	if bound, clarification := room.resolveWorkAnswer("For Release Review staging is fine."); bound.input != "" || clarification == "" {
		t.Fatalf("overlapping job titles bound an ambiguous answer: %+v, %q", bound, clarification)
	}
	room.callerQuestion = question
	if named, _ := room.resolveWorkAnswer("For Release Review, staging is fine."); named.job != review.ID || named.text != "staging is fine." {
		t.Fatalf("explicitly delimited answer was lost: %+v", named)
	}
	if named, _ := room.resolveWorkAnswer("För granska ändringar använd beta."); named.job != swedish.ID || named.input != swedish.InputID || named.text != "använd beta." {
		t.Fatalf("a Swedish title with different casing was not found: %+v", named)
	}
	if bound, clarification := room.resolveWorkAnswer("For Deploy check, Use beta."); bound.input != "" || clarification == "" {
		t.Fatalf("ambiguous title accepted: %+v, %q", bound, clarification)
	}
	if bound, clarification := room.resolveWorkAnswer("My answer is beta."); bound.input != "" || clarification == "" {
		t.Fatalf("unbound answer did not ask for the job: %+v, %q", bound, clarification)
	}
	room.callerQuestion = question
	room.workAnswer, _ = room.resolveWorkAnswer("For now, use beta.")
	if room.workAnswer.voiceWorkQuestion != question || room.workAnswer.text != "For now, use beta." {
		t.Fatalf("an ordinary reply starting with 'for' lost its question: %+v", room.workAnswer)
	}
	if result := answer(review.ID, review.InputID, "For now, use beta."); !strings.Contains(result, "answer was saved") {
		t.Fatal(result)
	}
	waitFor(t, ctx, func() bool {
		updated, _ := store.WorkDetails(ctx, chat.ID, review.ID)
		return updated.Status == tasks.Succeeded
	})
	room.startedAt = time.Now()
	if updated, err := store.WorkDetails(ctx, chat.ID, deploy.ID); err != nil || updated.Status != tasks.NeedsInput || updated.InputID != deploy.InputID {
		t.Fatalf("answer affected a different job: %+v, %v", updated, err)
	}
	room.callerQuestion = voiceWorkQuestion{deploy.ID, deploy.InputID, deploy.Title, deploy.Question}
	if bound, clarification := room.resolveWorkAnswer("For Release review, use stable."); bound.input != "" || clarification == "" {
		t.Fatalf("an answer for a finished job was offered to a different job: %+v, %q", bound, clarification)
	}
	room.callerQuestion = voiceWorkQuestion{deploy.ID, deploy.InputID, deploy.Title, deploy.Question}
	if bound, clarification := room.resolveWorkAnswer("For Some other job, use stable."); bound.input != "" || clarification != "" {
		t.Fatalf("an unknown title fell through to the spoken question: %+v, %q", bound, clarification)
	}
	room.callerQuestion = voiceWorkQuestion{deploy.ID, deploy.InputID, deploy.Title, deploy.Question}
	if bound, clarification := room.resolveWorkAnswer(ordinaryRequest); bound.input != "" || clarification != "" {
		t.Fatalf("a new request was handled as an answer to the pending question: %+v, %q", bound, clarification)
	}
	room.callerQuestion = voiceWorkQuestion{deploy.ID, deploy.InputID, deploy.Title, deploy.Question}
	if bound, _ := room.resolveWorkAnswer("For Deploy check, use stable."); bound.input != deploy.InputID {
		t.Fatalf("the played question did not disambiguate duplicate titles: %+v", bound)
	}
	if err := room.handleSpeech(member, realtimeEvent{Type: "conversation.item.input_audio_transcription.failed", Item: "answer-audio"}); err != nil {
		t.Fatal(err)
	}
	room.spokenQuestion = voiceWorkQuestion{deploy.ID, deploy.InputID, deploy.Title, deploy.Question}
	room.spokenQuestionPending = true
	room.observedWork = map[string]workObservation{review.ID: {status: tasks.Succeeded}}
	if _, err := store.AnswerWorkInput(ctx, chat.ID, deploy.InputID, "Use stable.", "from-another-device", nil); err != nil {
		t.Fatal(err)
	}
	worker.WakeBackground()
	waitFor(t, ctx, func() bool {
		updated, _ := store.WorkDetails(ctx, chat.ID, deploy.ID)
		return updated.Status == tasks.NeedsInput && updated.InputID != deploy.InputID && updated.Question == "Which build should I review?"
	})
	if err := room.syncTeam(); err != nil {
		t.Fatal(err)
	}
	if room.spokenQuestionPending || !room.requested || room.replyQuestion.input == "" || room.replyQuestion.input == deploy.InputID {
		t.Fatalf("an answered question blocked the next call update: %+v", room.replyQuestion)
	}
	room.callerQuestion = voiceWorkQuestion{deploy.ID, deploy.InputID, deploy.Title, deploy.Question}
	if bound, clarification := room.resolveWorkAnswer("For Deploy check, use stable."); bound.input != "" || !strings.Contains(clarification, "no longer pending") {
		t.Fatalf("stale spoken answer was rebound: %+v, %q", bound, clarification)
	}
}
