package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"time"

	"buddy/internal/localconfig"

	"golang.org/x/sys/unix"
)

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
