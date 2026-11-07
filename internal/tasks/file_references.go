package tasks

import (
	"context"
	"database/sql"
	"errors"
	"regexp"

	"buddy/internal/references"
)

const fileReferenceSchema = `
CREATE TABLE IF NOT EXISTS file_reference_workspaces (
	task_id TEXT NOT NULL REFERENCES tasks(id),
	bud_id TEXT NOT NULL REFERENCES agents(id),
	workspace TEXT NOT NULL,
	PRIMARY KEY(task_id,bud_id)
);
INSERT OR IGNORE INTO file_reference_workspaces
SELECT t.id,COALESCE(td.agent_id,c.agent_id),t.repo FROM tasks t
JOIN chat_turns ct ON ct.task_id=t.id JOIN conversations c ON c.id=ct.conversation_id
LEFT JOIN turn_details td ON td.task_id=t.id WHERE t.repo<>'';
`

var fileReferenceURL = regexp.MustCompile(`buddy://files/[A-Za-z0-9_-]+\?[^\s<>()]+`)

// Capture the producing task's workspace, including for older unbound URLs.
// References to a teammate capture that teammate's workspace when posted.
func captureFileReferences(ctx context.Context, db writeQueryer, task, text string) error {
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO file_reference_workspaces
		SELECT t.id,COALESCE(td.agent_id,c.agent_id),t.repo FROM tasks t
		JOIN chat_turns ct ON ct.task_id=t.id JOIN conversations c ON c.id=ct.conversation_id
		LEFT JOIN turn_details td ON td.task_id=t.id WHERE t.id=? AND t.repo<>''`, task); err != nil {
		return err
	}
	for _, raw := range fileReferenceURL.FindAllString(text, -1) {
		target, ok := references.Parse(raw)
		if !ok || target.Kind != "files" || target.Task != "" && target.Task != task {
			continue
		}
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO file_reference_workspaces
			SELECT ?,a.id,a.workspace FROM agents a WHERE a.id=? AND a.workspace<>''
			AND a.id<>(SELECT COALESCE(td.agent_id,c.agent_id) FROM chat_turns ct
			JOIN conversations c ON c.id=ct.conversation_id LEFT JOIN turn_details td ON td.task_id=ct.task_id
			WHERE ct.task_id=?)`, task, target.ID, task); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReferenceWorkspace(ctx context.Context, bud, task string) (string, error) {
	var workspace string
	err := s.db.QueryRowContext(ctx, `SELECT workspace FROM file_reference_workspaces
		WHERE bud_id=? AND task_id=?`, bud, task).Scan(&workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &InputError{"This file reference has no saved workspace. Open the file from its Bud’s workspace."}
	}
	return workspace, err
}
