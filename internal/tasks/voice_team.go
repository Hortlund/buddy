package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const voiceTeamSchema = `
CREATE TABLE IF NOT EXISTS voice_team_runs (
	call_id TEXT PRIMARY KEY,
	released_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS voice_team_steps (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	call_id TEXT NOT NULL REFERENCES voice_team_runs(call_id),
	previous_task TEXT REFERENCES tasks(id),
	wrap_up INTEGER NOT NULL
);
`

// QueueVoiceTeam saves the whole round before acknowledging it. The background
// worker can pick it up once the call closes, even if the app disconnects.
func (s *Store) QueueVoiceTeam(ctx context.Context, chat, callID, title, brief string, ids []string, allowEdits bool) error {
	title, brief = strings.TrimSpace(title), strings.TrimSpace(brief)
	if !validTitle(title, 100) || brief == "" || len(brief) > 10000 || len(ids) < 2 || len(ids) > 8 {
		return &InputError{"Give the team a clear assignment and choose at least two Buds."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var leadID string
	if err := tx.QueryRowContext(ctx, `SELECT c.agent_id FROM group_chats g
		JOIN conversations c ON c.id=g.conversation_id WHERE c.id=?`, chat).Scan(&leadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &InputError{"Invite another Bud before continuing in a group chat."}
		}
		return err
	}
	inserted, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO voice_team_runs(call_id) VALUES (?)", callID)
	if err != nil {
		return err
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return &InputError{"The team is already queued to continue this call in chat."}
	}
	var queued int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE status='queued' AND kind=?", BackgroundWork).Scan(&queued); err != nil {
		return err
	}
	if queued+len(ids)+1 > 100 {
		return ErrQueueFull
	}
	agents := make([]Agent, 0, len(ids))
	for i, id := range ids {
		if slices.Contains(ids[:i], id) {
			return &InputError{"Choose distinct Buds from the group."}
		}
		agent, err := conversationParticipant(ctx, tx, chat, id)
		if err != nil {
			return err
		}
		agent.AllowEdits = agent.AllowEdits && allowEdits
		agents = append(agents, agent)
	}
	lead := agents[0]
	for _, agent := range agents {
		if agent.ID == leadID {
			lead = agent
			break
		}
	}
	agents = append(agents, lead)
	previous := ""
	for i, agent := range agents {
		wrapUp := i == len(agents)-1
		stepTitle := title
		if wrapUp {
			stepTitle = "Wrap up: " + title
			agent.AllowEdits = false
		}
		task, err := enqueueTurn(ctx, tx, BackgroundWork, fmt.Sprintf("voice-team:%s:%d", callID, i), chat, agent,
			workRequest{origin: "handoff", title: stepTitle, brief: brief})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO voice_team_steps VALUES (?,?,NULLIF(?,''),?)",
			task.ID, callID, previous, wrapUp); err != nil {
			return err
		}
		previous = task.ID
	}
	return tx.Commit()
}

func (s *Store) ReleaseVoiceTeam(ctx context.Context, callID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE voice_team_runs SET released_at=? WHERE call_id=? AND released_at=0",
		time.Now().UnixMicro(), callID)
	return err
}
