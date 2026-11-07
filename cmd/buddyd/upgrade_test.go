package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buddy/internal/tasks"
)

func TestUpgradeReservesAnIdleQueue(t *testing.T) {
	ctx, dir := t.Context(), filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "buddy.db")
	store, err := tasks.OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := withIdleQueue(ctx, dir, false, func() error {
		competing, err := sql.Open("sqlite", path)
		if err != nil {
			return err
		}
		defer competing.Close()
		if _, err := competing.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
			t.Fatalf("competing writer should be blocked during shutdown, got %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Send(ctx, tasks.DefaultConversationID, "pending request", ""); err != nil {
		t.Fatal(err)
	}
	if err := withIdleQueue(ctx, dir, false, func() error {
		t.Fatal("an upgrade must not stop a daemon with queued work")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "active or queued work") {
		t.Fatalf("busy queue returned %v", err)
	}
	if err := withIdleQueue(ctx, dir, true, func() error { return nil }); err != nil {
		t.Fatalf("move should leave queued work in the database: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "UPDATE tasks SET status='running' WHERE status='queued'"); err != nil {
		t.Fatal(err)
	}
	if err := withIdleQueue(ctx, dir, true, func() error {
		t.Fatal("move must wait for running work")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "running work") {
		t.Fatalf("running work returned %v", err)
	}

}
