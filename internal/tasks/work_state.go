package tasks

import (
	"context"
	"time"
)

type WorkState struct {
	TeamAssignment
	InputID string `json:"input_id,omitempty"`
}

// WorkStates is the call's notification feed, independent of prompt/UI budgets.
// Include unfinished work and every job that finished after the call began.
func (s *Store) WorkStates(ctx context.Context, chat string, since time.Time) ([]WorkState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,td.agent_id,a.name,td.title,t.status,t.created,
		COALESCE((SELECT i.task_id FROM work_inputs i WHERE i.job_id=t.id AND i.answer_key IS NULL), '')
		FROM tasks t JOIN turn_details td ON td.task_id=t.id JOIN chat_turns ct ON ct.task_id=t.id
		JOIN agents a ON a.id=td.agent_id WHERE ct.conversation_id=? AND t.kind='background_work'
		AND (t.status IN ('queued','running','needs_input','cancelling') OR t.finished>=?)
		ORDER BY (t.status='needs_input') DESC,t.rowid`, chat, since.UnixMicro())
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
