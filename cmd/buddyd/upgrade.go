package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"

	"golang.org/x/sys/unix"
)

func updatePending(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".update-lock"))
	// Fail closed if the marker cannot be inspected.
	return !errors.Is(err, os.ErrNotExist)
}

// A candidate can expose health checks for the updater without accepting work
// or running routines until the updater commits by removing its marker.
func waitForUpdate(ctx context.Context, dir string) bool {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for updatePending(dir) {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
	return ctx.Err() == nil
}

func updateGate(dir string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && r.URL.Path != api.UpdateReadinessPath && updatePending(dir) {
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Cache-Control", "no-store")
			if r.URL.Path == "/readyz" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintln(w, `{"service":"buddy","status":"unavailable"}`)
				return
			}
			http.Error(w, "Buddy is finishing a server update. Reconnect in a moment.", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// The updater needs to recover an unchanged installation when waiting for a
// signaled daemon fails, while leaving a busy daemon untouched. Keep this exit
// code in sync with script/buddy-server.sh.
const stopRequestedExitCode = 3

type stopRequestedError struct{ error }

func (e *stopRequestedError) Unwrap() error { return e.error }

func stopErrorExitCode(err error) int {
	var requested *stopRequestedError
	if errors.As(err, &requested) {
		return stopRequestedExitCode
	}
	return 1
}

// Managed hosted installs use Restart=on-failure, so a graceful SIGTERM leaves
// the service stopped. Hold the queue reservation until server.lock is released.
func stopIdlePID(ctx context.Context, dir string, pid int) (err error) {
	if pid <= 1 || pid == os.Getpid() {
		return errors.New("invalid managed server PID")
	}
	if lock, err := localconfig.LockDir(dir); err == nil {
		lock.Close()
		return errors.New("the managed Buddy server is already stopped")
	} else if !errors.Is(err, unix.EWOULDBLOCK) {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop managed Buddy server: %w", err)
	}
	defer func() {
		if err != nil {
			err = &stopRequestedError{err}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if lock, err := localconfig.LockDir(dir); err == nil {
			return lock.Close()
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for managed Buddy server to stop: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Keep the previous daemon from accepting or claiming work between the idle
// check and shutdown. The existing database is opened without a migration.
func withIdleQueue(ctx context.Context, dir string, allowQueued bool, stop func() error) error {
	dir, err := localconfig.PrivateDir(dir)
	if err != nil {
		return err
	}
	dsn := url.URL{Scheme: "file", Path: filepath.Join(dir, "buddy.db"),
		RawQuery: "mode=rw&_txlock=immediate&_pragma=busy_timeout%282000%29"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reserve Buddy's queue before stopping the server: %w", err)
	}
	defer tx.Rollback()
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM tasks WHERE status IN ('running','cancelling') OR (status='queued' AND NOT ?))`, allowQueued).Scan(&busy); err != nil {
		return fmt.Errorf("check Buddy's queue before stopping the server: %w", err)
	}
	if busy && allowQueued {
		return errors.New("Buddy has running work. Wait for it to finish before moving the server.")
	}
	if busy {
		return errors.New("Buddy has active or queued work. Try again after it finishes.")
	}
	return stop()
}

func stopLaunchdService(ctx context.Context, dir, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/bin/launchctl", "bootout", target).CombinedOutput(); err != nil {
		return fmt.Errorf("stop Buddy's launchd service: %w (%s)", err, output)
	}
	// bootout can return while the service is still registered and holding
	// server.lock. Keep the queue reserved until the daemon has exited.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if exec.CommandContext(ctx, "/bin/launchctl", "print", target).Run() != nil && ctx.Err() == nil {
			lock, err := localconfig.LockDir(dir)
			if err == nil {
				return lock.Close()
			}
			if !errors.Is(err, unix.EWOULDBLOCK) {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for Buddy's launchd service to stop: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
