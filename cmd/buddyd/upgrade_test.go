package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestUpdateGateKeepsCandidateIdleUntilCommitted(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ".update-lock")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	handler := updateGate(dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for path, expected := range map[string]int{"/healthz": 200, "/readyz": 503, api.UpdateReadinessPath: 200, "/v1/conversations/main": 503} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != expected {
			t.Fatalf("%s: got %d, want %d", path, response.Code, expected)
		}
		if expected == 503 && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: maintenance response can be cached", path)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resumed := make(chan bool, 1)
	go func() { resumed <- waitForUpdate(ctx, dir) }()
	select {
	case <-resumed:
		t.Fatal("candidate started work before update committed")
	case <-time.After(20 * time.Millisecond):
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	select {
	case ready := <-resumed:
		if !ready {
			t.Fatal("committed update did not resume")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("candidate did not resume after commit")
	}
	for _, path := range []string{"/readyz", "/v1/conversations/main"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != 200 {
			t.Fatalf("%s remained gated after commit", path)
		}
	}
}

func TestUpdateWaitCancelsWithoutRunningWork(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".update-lock"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if waitForUpdate(ctx, dir) {
		t.Fatal("cancelled update resumed work")
	}
}

func TestStopIdlePIDHelper(t *testing.T) {
	dir := os.Getenv("BUDDY_UPGRADE_TEST_STATE")
	if dir == "" {
		return
	}
	lock, err := localconfig.LockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	fmt.Println("ready")
	<-signals
	if os.Getenv("BUDDY_UPGRADE_TEST_DELAY_STOP") == "1" {
		fmt.Println("stopping")
		for {
			if _, err := os.Stat(filepath.Join(dir, "release-stop")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestStopIdlePIDReportsFailureAfterSignal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStopIdlePIDHelper$")
	child.Env = append(os.Environ(), "BUDDY_UPGRADE_TEST_STATE="+dir, "BUDDY_UPGRADE_TEST_DELAY_STOP=1")
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("daemon fixture did not acquire its lock")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- stopIdlePID(ctx, dir, child.Process.Pid) }()
	if !scanner.Scan() || scanner.Text() != "stopping" {
		t.Fatal("daemon fixture did not receive SIGTERM")
	}
	cancel()
	err = <-stopped
	if !errors.Is(err, context.Canceled) || stopErrorExitCode(err) != stopRequestedExitCode {
		t.Fatalf("post-signal failure did not identify the stopped service: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-stop"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if code := stopErrorExitCode(stopIdlePID(t.Context(), dir, 1)); code == stopRequestedExitCode {
		t.Fatal("pre-signal failure requested a service restart")
	}
}

func TestStopIdlePIDWaitsForManagedDaemonLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(t.Context(), filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStopIdlePIDHelper$")
	child.Env = append(os.Environ(), "BUDDY_UPGRADE_TEST_STATE="+dir)
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("managed daemon fixture did not acquire its lock")
	}
	if err := withIdleQueue(t.Context(), dir, false, func() error { return stopIdlePID(t.Context(), dir, child.Process.Pid) }); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	lock, err := localconfig.LockDir(dir)
	if err != nil {
		t.Fatal("upgrade returned while server lock was held:", err)
	}
	lock.Close()
}

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
