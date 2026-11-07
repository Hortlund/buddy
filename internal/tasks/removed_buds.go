package tasks

import (
	"context"
	"database/sql"
	"errors"
)

const removedBudSchema = `
CREATE TABLE IF NOT EXISTS removed_buds (
	agent_id TEXT PRIMARY KEY REFERENCES agents(id)
);
` + dismissRemovedBudApprovals

const dismissRemovedBudApprovals = `
UPDATE standing_inbox SET state='dismissed',seen=1
WHERE state IN ('pending','approval') AND (
	bud IN (SELECT agent_id FROM removed_buds) OR
	watch_id IN (
		SELECT w.id FROM standing_watches w JOIN conversations c ON c.id=w.conversation
		JOIN removed_buds b ON b.agent_id=c.agent_id WHERE w.enabled=-1
	)
);
`

func agentRemoved(ctx context.Context, db queryer, id string) (bool, error) {
	var removed bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=a.id)
		FROM agents a WHERE a.id=?`, id).Scan(&removed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return removed, err
}

// RemoveAgent retires a Bud and its schedule while retaining its workspace
// and the records that identify it in chat and work history.
func (s *Store) RemoveAgent(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	removed, err := agentRemoved(ctx, tx, id)
	if err != nil || removed {
		return err
	}
	var replacement string
	err = tx.QueryRowContext(ctx, `SELECT a.id FROM agents a
		WHERE a.id<>? AND NOT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=a.id)
		ORDER BY a.name COLLATE NOCASE,a.id LIMIT 1`, id).Scan(&replacement)
	if errors.Is(err, sql.ErrNoRows) {
		return &InputError{"Keep at least one Bud. Create another Bud before removing this one."}
	}
	if err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_turns ct
		JOIN tasks t ON t.id=ct.task_id JOIN conversations c ON c.id=ct.conversation_id
		LEFT JOIN turn_details td ON td.task_id=t.id
		WHERE COALESCE(td.agent_id,c.agent_id)=? AND t.status IN ('queued','running','needs_input','cancelling'))`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return &InputError{"Finish or stop this Bud’s active work before removing it."}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO removed_buds VALUES (?)", id); err != nil {
		return err
	}
	if err := recordTeamEvent(ctx, tx, "left_team", id, ""); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE workspace_primary SET agent_id=? WHERE agent_id=?", replacement, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM group_members WHERE agent_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations AS c SET agent_id=COALESCE(
		(SELECT gm.agent_id FROM group_members gm WHERE gm.conversation_id=c.id
			ORDER BY (gm.agent_id=(SELECT agent_id FROM workspace_primary WHERE id=1)) DESC,gm.position LIMIT 1),c.agent_id)
		WHERE c.agent_id=? AND EXISTS(SELECT 1 FROM group_chats WHERE conversation_id=c.id)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE routines SET enabled=0 WHERE agent_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE standing_watches SET enabled=-1 WHERE conversation IN
		(SELECT id FROM conversations WHERE agent_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, dismissRemovedBudApprovals); err != nil {
		return err
	}
	return tx.Commit()
}
