package tasks

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// Snapshot copies committed SQLite state, including the WAL. The caller holds
// the daemon lock while it also archives the managed workspace files.
func (s *Store) Snapshot(ctx context.Context, destination string) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return err
	}
	return scrubMobileSnapshot(ctx, destination)
}

// SnapshotOnline reads the WAL through a separate connection so long copies
// leave the server's connection available for API requests and worker writes.
func (s *Store) SnapshotOnline(ctx context.Context, destination string) error {
	path, err := filepath.Abs(s.dbPath)
	if err != nil {
		return err
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout%283000%29"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return err
	}
	return scrubMobileSnapshot(ctx, destination)
}

const resetMobileAccess = "DELETE FROM mobile_delivery; DELETE FROM mobile_clients; DELETE FROM mobile_pairings;"

func scrubMobileSnapshot(ctx context.Context, destination string) error {
	// The snapshot gets its own connection. Source pairings stay valid.
	if err := scrubMobileCopy(ctx, destination); err != nil {
		return errors.Join(err, os.Remove(destination))
	}
	return nil
}

func scrubMobileCopy(ctx context.Context, destination string) error {
	db, err := sql.Open("sqlite", destination)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "PRAGMA secure_delete=ON; "+resetMobileAccess)
	return err
}

func (s *Store) ManagedWorkspaceRoot(ctx context.Context) (string, error) {
	var root string
	err := s.db.QueryRowContext(ctx, "SELECT path FROM managed_workspace_root WHERE id=1").Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return root, err
}

const WorkspaceReviewMessage = "Confirm this agent's working folder in Agent settings after moving Buddy."

// PrepareRestoredHome runs on the restored copy before it can be started.
// A custom path on the destination may name a different project.
func (s *Store) PrepareRestoredHome(ctx context.Context) error {
	root, err := s.ManagedWorkspaceRoot(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prefix := root + "/"
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO restored_workspace_agents
		SELECT id,workspace FROM agents WHERE workspace<>'' AND
		(?='' OR (workspace<>? AND substr(workspace,1,length(?))<>?))`, root, root, prefix, prefix); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO restored_workspace_tasks
		SELECT t.id,h.agent_id FROM tasks t JOIN chat_turns ct ON ct.task_id=t.id
		JOIN conversations c ON c.id=ct.conversation_id LEFT JOIN turn_details td ON td.task_id=t.id
		JOIN restored_workspace_agents h ON h.agent_id=COALESCE(td.agent_id,c.agent_id) AND h.path=t.repo
		WHERE t.status IN ('queued','needs_input') AND t.kind<>'work_input'
		AND NOT EXISTS(SELECT 1 FROM device_runs dr WHERE dr.task_id=t.id)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET summary=? WHERE id IN (SELECT task_id FROM restored_workspace_tasks)
		AND status='queued'`, WorkspaceReviewMessage); err != nil {
		return err
	}
	// Standalone repo checks and jobs for a previous agent folder have no
	// current agent binding to confirm. Preserve their history for a retry.
	const retry = "Buddy moved before this job ran. Submit it again with a working folder on this server."
	if err := interruptJobs(ctx, tx, `SELECT id FROM tasks t WHERE t.status IN ('queued','needs_input') AND t.repo<>''
		AND (?='' OR (t.repo<>? AND substr(t.repo,1,length(?))<>?))
		AND NOT EXISTS(SELECT 1 FROM restored_workspace_tasks h WHERE h.task_id=t.id)
		AND NOT EXISTS(SELECT 1 FROM device_runs dr WHERE dr.task_id=t.id)`, retry, root, root, prefix, prefix); err != nil {
		return err
	}
	// Backups made by earlier servers can still contain mobile credentials.
	if _, err := tx.ExecContext(ctx, resetMobileAccess); err != nil {
		return err
	}
	return tx.Commit()
}

func confirmRestoredWorkspace(ctx context.Context, tx *sql.Tx, agent Agent) error {
	const held = `SELECT h.task_id FROM restored_workspace_tasks h JOIN tasks t ON t.id=h.task_id
		WHERE h.agent_id=? AND t.status IN ('queued','needs_input')`
	if _, err := tx.ExecContext(ctx, "UPDATE chat_turns SET allow_edits=allow_edits AND ? WHERE task_id IN ("+held+")",
		agent.AllowEdits, agent.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET repo=?,summary=CASE WHEN status='queued' THEN ? ELSE summary END WHERE id IN ("+held+")",
		agent.Workspace, "Waiting to start.", agent.ID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM restored_workspace_agents WHERE agent_id=?", agent.ID)
	return err
}
