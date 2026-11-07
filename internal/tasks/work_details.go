package tasks

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

const workQuestionsSchema = `
CREATE TABLE IF NOT EXISTS work_questions (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	job_id TEXT NOT NULL REFERENCES tasks(id)
);
`

type WorkDetails struct {
	WorkItem
	Brief    string  `json:"brief"`
	Result   string  `json:"result,omitempty"`
	Question string  `json:"question,omitempty"`
	Events   []Event `json:"events,omitempty"`
}

func (s *Store) WorkDetails(ctx context.Context, chat, id string) (WorkDetails, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkDetails{}, err
	}
	defer tx.Rollback()

	items, err := readWork(ctx, tx, workSelect+" AND ct.conversation_id=? AND t.id=?", chat, id)
	if err != nil {
		return WorkDetails{}, err
	}
	if len(items) == 0 {
		return WorkDetails{}, ErrNotFound
	}
	detail := WorkDetails{WorkItem: items[0]}
	err = tx.QueryRowContext(ctx, `SELECT td.brief,COALESCE((SELECT m.text FROM messages m WHERE m.task_id=td.task_id
		AND m.role='assistant' ORDER BY m.id DESC LIMIT 1),''),
		COALESCE((SELECT m.text FROM messages m WHERE m.task_id=? AND m.role='assistant'),'')
		FROM turn_details td WHERE td.task_id=?`, detail.InputID, id).
		Scan(&detail.Brief, &detail.Result, &detail.Question)
	if err != nil {
		return WorkDetails{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,at,stream,substr(text,1,1500) FROM events WHERE task_id=?
		AND stream IN ('commentary','work_update','activity','system') ORDER BY seq DESC LIMIT 180`, id)
	if err != nil {
		return WorkDetails{}, err
	}
	defer rows.Close()
	// Older runners stored streaming prefixes alongside completed updates.
	// Keep the most complete entry and show the final answer only as Result.
	resultPrefix := strings.TrimSpace(detail.Result)
	var newestNote string
	var newestAt time.Time
	var seenUpdate bool
	for rows.Next() && len(detail.Events) < 60 {
		var event Event
		var at int64
		if err := rows.Scan(&event.Seq, &at, &event.Stream, &event.Text); err != nil {
			return WorkDetails{}, err
		}
		event.At = time.UnixMicro(at).UTC()
		event.Text = strings.TrimSpace(event.Text)
		if event.Text == "" {
			continue
		}
		if event.Stream == "commentary" {
			prefix := strings.TrimSpace(strings.TrimSuffix(event.Text, "…"))
			seenUpdate = true
			if prefix == "" || strings.HasPrefix(resultPrefix, prefix) ||
				(newestAt.Sub(event.At) <= 2*time.Second && strings.HasPrefix(newestNote, prefix)) {
				continue
			}
			newestNote, newestAt = event.Text, event.At
		} else {
			newestNote = ""
			if seenUpdate || event.Stream != "system" || event.Text != detail.Activity {
				resultPrefix = ""
			}
			if event.Stream == "work_update" {
				event.Stream = "commentary"
			}
		}
		detail.Events = append(detail.Events, event)
	}
	slices.Reverse(detail.Events)
	if err := rows.Close(); err != nil {
		return WorkDetails{}, err
	}
	if err := rows.Err(); err != nil {
		return WorkDetails{}, err
	}
	return detail, tx.Commit()
}
