package tasks

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMobilePushDropsAnsweredQuestionBeforeNextQuestion(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pair, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, pair.Code, "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMobileNotifications(ctx, phone.ID, "abcd", false); err != nil {
		t.Fatal(err)
	}
	watch, err := store.CreateWatch(ctx, Watch{
		ConversationID: DefaultConversationID, Name: "CI", Source: GitHubCI,
		Scope: "org/buddy", Mode: WatchInvestigate, Budget: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordStandingSignal(ctx, StandingSignal{
		Source: watch.Source, Scope: watch.Scope, Key: "1:1", Title: "CI failed",
	}); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueueStandingWatch(ctx); err != nil || !queued {
		t.Fatalf("watch queue: %v, %v", queued, err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, job.ID, NeedsInput, "Waiting", "First question", nil); err != nil {
		t.Fatal(err)
	}
	first, err := store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || first.InputID == "" {
		t.Fatalf("first question: %q %v", first.InputID, err)
	}
	if _, err := store.AnswerWorkInput(ctx, DefaultConversationID, first.InputID, "First answer", "fixture-key", nil); err != nil {
		t.Fatal(err)
	}
	if next, err := store.claimLane(ctx, true); err != nil || next.ID != job.ID {
		t.Fatalf("next job: %q %v", next.ID, err)
	}
	if err := store.finishTurn(ctx, job.ID, NeedsInput, "Waiting", "Second question", nil); err != nil {
		t.Fatal(err)
	}
	second, err := store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || second.InputID == "" || second.InputID == first.InputID {
		t.Fatalf("second question: %q %v", second.InputID, err)
	}
	notice, err := store.NextMobileDelivery(ctx, time.Now())
	if err != nil || notice.Item != job.ID || notice.Kind != "needs_input" {
		t.Fatalf("wrong push route: %q, %q, %v", notice.Item, notice.Kind, err)
	}
	var question string
	if err := store.db.QueryRowContext(ctx, "SELECT item FROM mobile_delivery WHERE id=?", notice.ID).Scan(&question); err != nil || question != second.InputID {
		t.Fatalf("push still references the first question: %q, %v", question, err)
	}
	if err := store.CompleteMobileDelivery(ctx, notice, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextMobileDelivery(ctx, time.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("answered question still has a push alert: %v", err)
	}
}
