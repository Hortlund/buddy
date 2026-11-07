package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerRetriesSavingAReplyWithoutRerunning(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 8*time.Second)
	defer stop()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_reply BEFORE INSERT ON messages WHEN NEW.role='assistant'
		BEGIN SELECT RAISE(ABORT, 'temporary write failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	w := NewWorker(store, nil, func(context.Context, Turn, Emit) (string, error) {
		runs.Add(1)
		return "completed answer", nil
	})
	task, _, err := store.Send(ctx, DefaultConversationID, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	defer func() { stop(); <-done }()
	waitUntil(t, ctx, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		run := w.runs[task.ID]
		return run != nil && run.completing
	})
	// Keep the failure in place across a full retry interval.
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := store.db.ExecContext(ctx, "DROP TRIGGER reject_reply"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, ctx, func() bool {
		item, err := store.Get(ctx, task.ID)
		return err == nil && item.Status == Succeeded
	})
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 2 || chat.Messages[1].Text != "completed answer" || runs.Load() != 1 {
		t.Fatalf("reply or run count changed: %+v, runs=%d, %v", chat.Messages, runs.Load(), err)
	}
}

func TestProtectedRuntimeRejectsExistingHardlinks(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "server.identity")
	if err := os.WriteFile(protected, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateProtectedRoots([]string{root}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(protected, filepath.Join(t.TempDir(), "alias")); err != nil {
		t.Fatal(err)
	}
	if err := validateProtectedRoots([]string{root}, false); err == nil {
		t.Fatal("accepted a protected inode with a writable alias")
	}
}

func waitUntil(t *testing.T, ctx context.Context, check func() bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !check() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestRuntimePathsRejectGlobSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state[1]")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := protectedRoots([]string{path}); err == nil {
		t.Fatal("accepted a literal path which Codex would interpret as a glob")
	}
}

func TestSteeringTargetsActiveTurnAndPreservesQueuedFollowUp(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 6*time.Second)
	defer stop()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	w := NewWorker(store, nil, func(ctx context.Context, turn Turn, emit Emit) (string, error) {
		current := turn.Messages[len(turn.Messages)-1].Text
		if current == "follow up" {
			if len(turn.Messages) != 3 || len(turn.Messages[0].Steering) != 1 ||
				turn.Messages[0].Steering[0].State != steerApplied {
				return "", errors.New("the completed turn's steering was missing from context")
			}
			return "follow-up result", nil
		}
		turn.steering.ready(true)
		defer turn.steering.ready(false)
		close(started)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-turn.steering.wake:
		}
		correction, err := turn.steering.next(ctx)
		if err != nil {
			return "", err
		}
		if correction.Text != "adjust the current work" {
			return "", errors.New("the active turn did not get the correction")
		}
		if err := turn.steering.mark(correction.ID, steerApplied); err != nil {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return "adjusted result", nil
		}
	})

	active, _, err := store.Send(ctx, DefaultConversationID, "first request", "first")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	defer func() { stop(); <-done }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	queued, _, err := store.Send(ctx, DefaultConversationID, "follow up", "queued")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Steer(ctx, queued.ID, "wrong turn", "wrong"); !errors.Is(err, ErrSteeringUnavailable) {
		t.Fatalf("steering a queued turn returned %v", err)
	}
	if _, err := w.Steer(ctx, active.ID, "/review the current diff", "review-steering"); err == nil || !strings.Contains(err.Error(), "follow-up") {
		t.Fatalf("steering accepted a skill invocation: %v", err)
	}
	correction, err := w.Steer(ctx, active.ID, "adjust the current work", "same-correction")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := w.Steer(ctx, active.ID, "adjust the current work", "same-correction")
	if err != nil || retry.ID != correction.ID {
		t.Fatalf("retry duplicated steering: %+v, %v", retry, err)
	}
	item, err := store.Get(ctx, queued.ID)
	if err != nil || item.Status != Queued {
		t.Fatalf("follow-up did not wait for active run: %+v, %v", item, err)
	}
	close(release)
	waitUntil(t, ctx, func() bool {
		item, err := store.Get(ctx, queued.ID)
		return err == nil && item.Status == Succeeded
	})
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 4 || chat.Messages[3].Text != "follow-up result" ||
		len(chat.Messages[0].Steering) != 1 || chat.Messages[0].Steering[0].State != steerApplied {
		t.Fatalf("unexpected conversation after steering: %+v, %v", chat.Messages, err)
	}
	retry, err = w.Steer(ctx, active.ID, "adjust the current work", "same-correction")
	if err != nil || retry.ID != correction.ID || retry.State != steerApplied {
		t.Fatalf("late retry did not return the original correction: %+v, %v", retry, err)
	}
}
