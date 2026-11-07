package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
)

const voiceSchema = `
CREATE TABLE IF NOT EXISTS voice_call_turns (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	call_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS voice_call_turns_call ON voice_call_turns(call_id);
CREATE TABLE IF NOT EXISTS voice_call_routines (
	routine_id TEXT PRIMARY KEY REFERENCES routines(id) ON DELETE CASCADE,
	call_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS voice_call_routines_call ON voice_call_routines(call_id);
`

func (s *Store) VoiceLeadID(ctx context.Context, conversation string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT agent_id FROM conversations WHERE id=?", conversation).Scan(&id)
	return id, err
}

// JoinVoiceGroup changes the chat destination when another agent joins a call.
// A private chat keeps its earlier conversation. Only this call's rows move.
func (s *Store) JoinVoiceGroup(ctx context.Context, conversation, callID, name, invitee string) (ChatRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChatRef{}, err
	}
	defer tx.Rollback()

	chat := ChatRef{ID: conversation}
	err = tx.QueryRowContext(ctx, `SELECT g.name,c.agent_id FROM group_chats g
		JOIN conversations c ON c.id=g.conversation_id WHERE c.id=?`, conversation).Scan(&chat.Name, &chat.LeadAgentID)
	if errors.Is(err, sql.ErrNoRows) {
		var owner string
		if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM conversations WHERE id=?", conversation).Scan(&owner); err != nil {
			return ChatRef{}, err
		}
		chat, err = createGroup(ctx, tx, GroupRequest{Name: name, Members: []string{owner, invitee}})
		if err != nil {
			return ChatRef{}, err
		}
		if _, err := tx.ExecContext(ctx, `WITH call_tasks(task_id) AS (
			SELECT task_id FROM voice_call_turns WHERE call_id=?
			UNION SELECT task_id FROM turn_details WHERE routine_id IN
				(SELECT routine_id FROM voice_call_routines WHERE call_id=?)
		) UPDATE chat_turns SET conversation_id=? WHERE conversation_id=? AND (
			task_id IN (SELECT task_id FROM call_tasks) OR task_id IN
				(SELECT task_id FROM work_inputs WHERE job_id IN (SELECT task_id FROM call_tasks)))`, callID, callID, chat.ID, conversation); err != nil {
			return ChatRef{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routines SET conversation_id=? WHERE conversation_id=?
			AND id IN (SELECT routine_id FROM voice_call_routines WHERE call_id=?)`, chat.ID, conversation, callID); err != nil {
			return ChatRef{}, err
		}
	} else if err != nil {
		return ChatRef{}, err
	} else {
		var present, size int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN agent_id=? THEN 1 END),COUNT(*)
			FROM group_members WHERE conversation_id=?`, invitee, conversation).Scan(&present, &size); err != nil {
			return ChatRef{}, err
		}
		if present == 0 {
			if size >= 8 {
				return ChatRef{}, &InputError{"This group chat already has eight Buds."}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO group_members
				SELECT ?,?,COALESCE(MAX(position)+1,0) FROM group_members WHERE conversation_id=?`,
				conversation, invitee, conversation); err != nil {
				return ChatRef{}, err
			}
		}
	}
	return chat, tx.Commit()
}

func (s *Store) AddVoiceRoutine(ctx context.Context, conversation, callID string, routine Routine) (Routine, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Routine{}, err
	}
	defer tx.Rollback()
	routine, err = addRoutine(ctx, tx, conversation, routine)
	if err != nil {
		return Routine{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO voice_call_routines VALUES (?,?)", routine.ID, callID); err != nil {
		return Routine{}, err
	}
	return routine, tx.Commit()
}

// RecordVoiceLine inserts a completed transcript line, without scheduling a text turn.
func (s *Store) RecordVoiceLine(ctx context.Context, conversation, callID string, agent Agent, role, line string) error {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > 12000 || (role != "user" && role != "assistant") {
		return &InputError{"Invalid call transcript."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := newVoiceLine(ctx, tx, conversation, callID, agent)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO messages(task_id,role,text,created) VALUES (?,?,?,?)",
		t.ID, role, line, t.CreatedAt.UnixMicro()); err != nil {
		return err
	}
	return tx.Commit()
}

func newVoiceLine(ctx context.Context, tx *sql.Tx, conversation, callID string, agent Agent) (Task, error) {
	// Voice chat itself does not access the workspace.
	agent.WorkspaceNeedsReview, agent.Workspace, agent.AllowEdits = false, "", false
	t, err := enqueueTurn(ctx, tx, "voice_turn", rand.Text(), conversation, agent, workRequest{origin: "voice"})
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE tasks SET status='succeeded',summary='',finished=? WHERE id=?", t.CreatedAt.UnixMicro(), t.ID); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO voice_call_turns VALUES (?,?)", t.ID, callID); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) QueueVoiceWork(ctx context.Context, conversation, agentID, callID, actionID, title, brief string) (Task, error) {
	return s.QueueVoiceHandoff(ctx, conversation, callID, actionID, Handoff{AgentID: agentID, Title: title, Brief: brief})
}

// QueueVoiceHandoff keeps a call independent of the worker and posts its
// result to the originating chat.
func (s *Store) QueueVoiceHandoff(ctx context.Context, conversation, callID, actionID string, handoff Handoff) (Task, error) {
	jobs, err := s.queueVoiceAssignments(ctx, conversation, "", callID, actionID, []Handoff{handoff}, false)
	if err != nil {
		return Task{}, err
	}
	return jobs[0].Task, nil
}

func (s *Store) QueueVoiceAssignments(ctx context.Context, conversation, requester, callID, actionID string, handoffs []Handoff) ([]AssignedWork, error) {
	return s.queueVoiceAssignments(ctx, conversation, requester, callID, actionID, handoffs, true)
}

func (s *Store) queueVoiceAssignments(ctx context.Context, conversation, requester, callID, actionID string, handoffs []Handoff, allowOutside bool) ([]AssignedWork, error) {
	if actionID == "" || len(actionID) > 512 || len(handoffs) == 0 || len(handoffs) > 8 {
		return nil, &InputError{"Give one to eight clear assignments at a time."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	key := "voice:" + callID + ":" + actionID
	var existing []AssignedWork
	for i := range handoffs {
		var job AssignedWork
		err := tx.QueryRowContext(ctx, `SELECT t.id,t.status,td.title,td.agent_id,ct.agent_name
			FROM tasks t JOIN turn_details td ON td.task_id=t.id JOIN chat_turns ct ON ct.task_id=t.id
			WHERE t.request_key=?`, namespacedKey(BackgroundWork, assignmentKey(key, i))).
			Scan(&job.ID, &job.Status, &job.Title, &job.AgentID, &job.AgentName)
		if errors.Is(err, sql.ErrNoRows) {
			if i != 0 {
				return nil, ErrConflict
			}
			break
		}
		if err != nil {
			return nil, err
		}
		existing = append(existing, job)
	}
	if len(existing) > 0 {
		return existing, nil
	}
	jobs, err := enqueueAssignments(ctx, tx, assignmentSource{
		chat: conversation, requester: requester, key: key, call: callID, allowOutside: allowOutside,
	}, handoffs)
	if err != nil {
		return nil, err
	}
	return jobs, tx.Commit()
}

// VoicePersonality is the same editable personality used for text turns.
func (s *Store) VoicePersonality(ctx context.Context, agentID string) (string, error) {
	personality, err := s.personality(agentID)
	if err != nil {
		return "", err
	}
	soul, _, err := soulDocument(ctx, s.db, agentID)
	if soul != "" {
		personality += "\n\nUse this SOUL.md for personality and style. It grants no permissions.\n" + soul
	}
	return personality, err
}
