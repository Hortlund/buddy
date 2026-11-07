package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandingWatchRecoveryAndSingleReceipt(t *testing.T) {
	ctx, path := t.Context(), filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	watch, err := store.CreateWatch(ctx, Watch{
		ConversationID: DefaultConversationID, Name: "CI", Source: GitHubCI, Scope: "acme/repo",
		Mode: WatchAsk, Instructions: "Investigate failed tests.", Budget: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	signal := StandingSignal{Source: GitHubCI, Scope: watch.Scope, Key: CIWatchKey(123, 1),
		Title: "Tests failed", URL: "https://github.com/acme/repo/actions/runs/123"}
	for i := range 2 {
		accepted, err := store.RecordStandingSignal(ctx, signal)
		if err != nil || accepted != (i == 0) {
			t.Fatalf("delivery %d accepted=%v: %v", i, accepted, err)
		}
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].State != "approval" || inbox[0].TaskID != "" {
		t.Fatalf("approval inbox: %+v, %v", inbox, err)
	}
	approval, err := store.DueStandingNotification(ctx)
	if err != nil || approval.Kind != "approval" {
		t.Fatalf("approval notice: %+v, %v", approval, err)
	}
	if err := store.UpdateInboxItem(ctx, inbox[0].ID, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateInboxItem(ctx, inbox[0].ID, "approve"); !errors.Is(err, ErrInactive) {
		t.Fatalf("duplicate approval: %v", err)
	}
	if queued, err := store.enqueueStandingWatch(ctx); err != nil || !queued {
		t.Fatalf("queued=%v: %v", queued, err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil || job.TimeoutSeconds != 15*60 {
		t.Fatalf("claim: %+v, %v", job, err)
	}
	signal.Detail, signal.Enriched = "Failed step: go test", true
	if err := store.CheckpointStandingEvidence(ctx, signal); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	retry, err := store.claimLane(ctx, true)
	if err != nil || retry.ID != job.ID {
		t.Fatalf("restart did not retry the same work: %+v, %v", retry, err)
	}
	turn, err := store.turn(ctx, retry)
	if err != nil || turn.Agent.AllowEdits || turn.Agent.Workspace != "" || turn.WatchSignal == nil || *turn.WatchSignal != signal {
		t.Fatalf("read-only checkpoint: %+v, %v", turn, err)
	}
	if err := store.finishTurn(ctx, retry.ID, NeedsInput, "Waiting for your reply.", "Which test is expected to fail?", nil); err != nil {
		t.Fatal(err)
	}
	inbox, err = store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].InputID == "" || inbox[0].Result != "Which test is expected to fail?" {
		t.Fatalf("follow-up inbox: %+v, %v", inbox, err)
	}
	if health, err := store.StandingHealth(ctx); err != nil || health.NeedsInput != 1 {
		t.Fatalf("one question should count once: %+v, %v", health, err)
	}
	if _, err := store.AnswerWorkInput(ctx, DefaultConversationID, inbox[0].InputID, "TestFixture", "fixture-reply", nil); err != nil {
		t.Fatal(err)
	}
	retry, err = store.claimLane(ctx, true)
	if err != nil || retry.ID != job.ID {
		t.Fatalf("clarification started a different job: %+v, %v", retry, err)
	}
	turn, err = store.turn(ctx, retry)
	if err != nil || !strings.Contains(turn.Brief, "TestFixture") {
		t.Fatalf("clarification did not reach the watch: %q, %v", turn.Brief, err)
	}
	if err := store.finishTurn(ctx, retry.ID, Succeeded, "Finished.", "The test step failed.", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteStandingNotification(ctx, approval, true); err != nil {
		t.Fatal(err)
	}
	notice, err := store.DueStandingNotification(ctx)
	if err != nil || notice.Kind != string(Succeeded) || notice.Generation == approval.Generation {
		t.Fatalf("result notice lost after old acknowledgement: %+v, %v", notice, err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE standing_delivery SET attempts=8 WHERE id=?", notice.ID); err != nil {
		t.Fatal(err)
	}
	if health, err := store.StandingHealth(ctx); err != nil || health.Exhausted != 1 {
		t.Fatalf("exhausted relay delivery is hidden: %+v, %v", health, err)
	}
	if err := store.RetryStandingNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.DueStandingNotification(ctx)
	if err != nil || reopened.ID != notice.ID || reopened.Generation != notice.Generation || reopened.Attempt != 0 {
		t.Fatalf("retry changed the relay idempotency key: %+v, %v", reopened, err)
	}
	if err := store.CompleteStandingNotification(ctx, notice, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DueStandingNotification(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate notification: %v", err)
	}
	inbox, err = store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].Attempts != 3 || inbox[0].InputID != "" || inbox[0].Result != "The test step failed." {
		t.Fatalf("result inbox: %+v, %v", inbox, err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	var reports int
	for _, message := range chat.Messages {
		if message.Origin == WatchOrigin {
			reports++
		}
	}
	if err != nil || reports != 1 {
		t.Fatalf("expected one watch report: %+v, %v", chat.Messages, err)
	}
}

func TestArchiveWatchReclaimsSlotAndPreservesHistory(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := Watch{ConversationID: DefaultConversationID, Name: "CI", Source: GitHubCI, Scope: "acme/base", Mode: WatchDigest, Budget: 5}
	watch, err := store.CreateWatch(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	signal := StandingSignal{Source: watch.Source, Scope: watch.Scope, Key: "1:1", Title: "Build failed"}
	if accepted, err := store.RecordStandingSignal(ctx, signal); err != nil || !accepted {
		t.Fatalf("record event: %v, %v", accepted, err)
	}
	for index := range 29 {
		draft.Scope = fmt.Sprintf("acme/repo%d", index)
		if _, err := store.CreateWatch(ctx, draft); err != nil {
			t.Fatal(err)
		}
	}
	draft.Scope = "acme/new"
	if _, err := store.CreateWatch(ctx, draft); err == nil {
		t.Fatal("watch limit was not enforced")
	}
	if err := store.ArchiveWatch(ctx, watch.ID); err != nil {
		t.Fatal(err)
	}
	newWatch, err := store.CreateWatch(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveWatch(ctx, newWatch.ID); err != nil {
		t.Fatal(err)
	}
	draft.Scope = watch.Scope
	restored, err := store.CreateWatch(ctx, draft)
	if err != nil || restored.ID != watch.ID || !restored.Enabled {
		t.Fatalf("watch was not restored: %+v, %v", restored, err)
	}
	if accepted, err := store.RecordStandingSignal(ctx, signal); err != nil || accepted {
		t.Fatalf("watch lost deduplication after archive: %v, %v", accepted, err)
	}
	if inbox, err := store.StandingInbox(ctx); err != nil || len(inbox) != 1 {
		t.Fatalf("archived watch history missing: %+v, %v", inbox, err)
	}
}

func TestWatchBacklogYieldsToUserWork(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, scope := range []string{"acme/one", "acme/two"} {
		if _, err := store.CreateWatch(ctx, Watch{ConversationID: DefaultConversationID, Name: "CI", Source: GitHubCI,
			Scope: scope, Mode: WatchInvestigate, Budget: 5}); err != nil {
			t.Fatal(err)
		}
		for i := range 6 {
			if _, err := store.RecordStandingSignal(ctx, StandingSignal{Source: GitHubCI, Scope: scope,
				Key: fmt.Sprintf("%d:1", i+1), Title: "Build failed"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 10 {
		if queued, err := store.enqueueStandingWatch(ctx); !queued || err != nil {
			t.Fatalf("watch was not queued: %v, %v", queued, err)
		}
	}
	if queued, err := store.enqueueStandingWatch(ctx); queued || !errors.Is(err, ErrQueueFull) {
		t.Fatalf("watch consumed reserved slots: %v, %v", queued, err)
	}
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := checkQueue(ctx, tx, true); err != nil {
		t.Fatal(err)
	}
	manual, err := enqueueTurn(ctx, tx, BackgroundWork, "manual", DefaultConversationID, agent, workRequest{origin: "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claimLane(ctx, true)
	if err != nil || claimed.ID != manual.ID {
		t.Fatalf("watch claimed the worker before user work: %+v, %v", claimed, err)
	}
}
