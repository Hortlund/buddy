package tasks

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const historySchema = `
CREATE TABLE IF NOT EXISTS archived_groups (
	conversation_id TEXT PRIMARY KEY REFERENCES group_chats(conversation_id)
);
CREATE TABLE IF NOT EXISTS archived_calls (
	call_id TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS transcript_revisions (
	conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
	revision INTEGER NOT NULL
);
`

const visibleTranscript = ` NOT EXISTS (SELECT 1 FROM voice_call_turns vc JOIN archived_calls ac ON ac.call_id=vc.call_id
	WHERE vc.task_id=t.id AND td.origin='voice') `

func requireOpenConversation(ctx context.Context, db queryer, chat string) error {
	var archived bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM archived_groups WHERE conversation_id=?)", chat).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return &InputError{"Restore this group chat before making changes."}
	}
	return nil
}

func (s *Store) SetGroupArchived(ctx context.Context, chat string, archived bool) error {
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM group_chats WHERE conversation_id=?)", chat).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if !archived {
		_, err = tx.ExecContext(ctx, "DELETE FROM archived_groups WHERE conversation_id=?", chat)
	} else {
		for _, statement := range []string{
			"INSERT OR IGNORE INTO archived_groups VALUES (?)",
			"UPDATE routines SET enabled=0 WHERE conversation_id=?",
			"UPDATE standing_watches SET enabled=0 WHERE conversation=? AND enabled=1",
			"UPDATE standing_inbox SET state='dismissed',seen=1 WHERE conversation=? AND state IN ('pending','approval')",
		} {
			if _, err := tx.ExecContext(ctx, statement, chat); err != nil {
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

type CallHistory struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Archived  bool      `json:"archived"`
}

func (s *Store) Calls(ctx context.Context, chat string) ([]CallHistory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT vc.call_id,min(t.created),max(t.created),ac.call_id IS NOT NULL FROM voice_call_turns vc
		JOIN chat_turns ct ON ct.task_id=vc.task_id JOIN turn_details td ON td.task_id=vc.task_id
		JOIN tasks t ON t.id=vc.task_id LEFT JOIN archived_calls ac ON ac.call_id=vc.call_id
		WHERE ct.conversation_id=? AND td.origin='voice'
		GROUP BY vc.call_id ORDER BY min(t.created) DESC`, chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CallHistory, 0)
	for rows.Next() {
		var item CallHistory
		var start, end int64
		if err := rows.Scan(&item.ID, &start, &end, &item.Archived); err != nil {
			return nil, err
		}
		item.StartedAt, item.EndedAt = time.UnixMicro(start).UTC(), time.UnixMicro(end).UTC()
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SetCallArchived(ctx context.Context, chat, call string, archived bool) error {
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found string
	err = tx.QueryRowContext(ctx, `SELECT vc.call_id FROM voice_call_turns vc JOIN chat_turns ct ON ct.task_id=vc.task_id
		JOIN turn_details td ON td.task_id=vc.task_id WHERE ct.conversation_id=? AND vc.call_id=? AND td.origin='voice' LIMIT 1`,
		chat, call).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	statement := "DELETE FROM archived_calls WHERE call_id=?"
	if archived {
		statement = "INSERT OR IGNORE INTO archived_calls VALUES (?)"
	}
	change, err := tx.ExecContext(ctx, statement, call)
	if err != nil {
		return err
	}
	count, err := change.RowsAffected()
	if err != nil {
		return err
	}
	if count != 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_revisions VALUES (?,1)
			ON CONFLICT(conversation_id) DO UPDATE SET revision=revision+1`, chat); err != nil {
			return err
		}
	}
	return tx.Commit()
}
