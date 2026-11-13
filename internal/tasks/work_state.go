package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"modernc.org/sqlite"
)

func init() {
	fold := cases.Fold()
	sqlite.MustRegisterCollationUtf8("buddy_unicode_nocase", func(left, right string) int {
		return strings.Compare(fold.String(left), fold.String(right))
	})
}

type WorkState struct {
	TeamAssignment
	InputID string `json:"input_id,omitempty"`
}

// WorkStates is the call's notification feed, independent of prompt/UI budgets.
// Include unfinished work and every job that finished after the call began.
func (s *Store) WorkStates(ctx context.Context, chat string, since time.Time) ([]WorkState, error) {
	return s.readWorkStates(ctx, chat, `AND (t.status IN ('queued','running','needs_input','cancelling') OR t.finished>=?)`, since.UnixMicro())
}

// WorkStatesByTitle also searches finished jobs so a stale spoken answer can
// never fall through to a different job's pending question.
func (s *Store) WorkStatesByTitle(ctx context.Context, chat string, titles []string) ([]WorkState, error) {
	if len(titles) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(titles)
	if err != nil {
		return nil, err
	}
	return s.readWorkStates(ctx, chat, `AND td.title COLLATE buddy_unicode_nocase IN (SELECT value FROM json_each(?))`, string(encoded))
}

func (s *Store) readWorkStates(ctx context.Context, chat, filter string, arg any) ([]WorkState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,td.agent_id,a.name,td.title,t.status,t.created,
		COALESCE((SELECT i.task_id FROM work_inputs i WHERE i.job_id=t.id AND i.answer_key IS NULL), '')
		FROM tasks t JOIN turn_details td ON td.task_id=t.id JOIN chat_turns ct ON ct.task_id=t.id
		JOIN agents a ON a.id=td.agent_id WHERE ct.conversation_id=? AND t.kind='background_work'
		`+filter+` ORDER BY (t.status='needs_input') DESC,t.rowid`, chat, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []WorkState
	for rows.Next() {
		var state WorkState
		var at int64
		if err := rows.Scan(&state.ID, &state.AgentID, &state.AgentName, &state.Title, &state.Status, &at, &state.InputID); err != nil {
			return nil, err
		}
		state.ConversationID, state.InThisChat = chat, true
		state.CreatedAt = time.UnixMicro(at).UTC()
		states = append(states, state)
	}
	return states, rows.Err()
}
