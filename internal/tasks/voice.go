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
			UNION SELECT task_id FROM assignment_reuses WHERE call_id=?
			UNION SELECT task_id FROM turn_details WHERE routine_id IN
				(SELECT routine_id FROM voice_call_routines WHERE call_id=?)
		) UPDATE chat_turns SET conversation_id=? WHERE conversation_id=? AND (
			task_id IN (SELECT task_id FROM call_tasks) OR task_id IN
				(SELECT task_id FROM work_inputs WHERE job_id IN (SELECT task_id FROM call_tasks)))`, callID, callID, callID, chat.ID, conversation); err != nil {
			return ChatRef{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE voice_call_turns SET call_id=? WHERE task_id IN
			(SELECT task_id FROM assignment_reuses WHERE call_id=?)`, callID, callID); err != nil {
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
			if err := recordTeamEvent(ctx, tx, "joined_group", invitee, conversation); err != nil {
				return ChatRef{}, err
			}
		}
	}
	return chat, tx.Commit()
}

func (s *Store) AddVoiceRoutine(ctx context.Context, conversation, callID string, routine Routine) (Routine, error) {
	saved, err := s.AddVoiceRoutines(ctx, conversation, callID, []Routine{routine})
	if err != nil {
		return Routine{}, err
	}
	return saved[0], nil
}

func (s *Store) AddVoiceRoutines(ctx context.Context, conversation, callID string, routines []Routine) ([]Routine, error) {
	if len(routines) == 0 || len(routines) > 8 {
		return nil, &InputError{"Schedule one to eight routines at a time."}
	}
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved := make([]Routine, 0, len(routines))
	for _, routine := range routines {
		routine, err = addRoutine(ctx, tx, conversation, routine)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO voice_call_routines VALUES (?,?)", routine.ID, callID); err != nil {
			return nil, err
		}
		saved = append(saved, routine)
	}
	return saved, tx.Commit()
}

// SaveVoiceBud attaches the result to the call so it follows the transcript
// when a one-to-one call becomes a group.
func (s *Store) SaveVoiceBud(ctx context.Context, chat, call, speaker, request string, draft BudDraft, handoffs ...Handoff) (Agent, []AssignedWork, string, error) {
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return Agent{}, nil, "", err
	}
	defer tx.Rollback()
	if err := requireOpenConversation(ctx, tx, chat); err != nil {
		return Agent{}, nil, "", err
	}
	agent, err := conversationParticipant(ctx, tx, chat, speaker)
	if err != nil {
		return Agent{}, nil, "", err
	}
	line, err := newVoiceLine(ctx, tx, chat, call, agent)
	if err != nil {
		return Agent{}, nil, "", err
	}
	profile, err := saveBudProposal(ctx, tx, line.ID, draft)
	if err != nil {
		return Agent{}, nil, "", err
	}
	var pending []Handoff
	if len(handoffs) != 0 {
		var independent []Handoff
		independent, pending, err = partitionBudWork(&TurnAction{Handoffs: handoffs})
		if err != nil {
			return Agent{}, nil, "", err
		}
		if len(independent) != 0 {
			return Agent{}, nil, "", &InputError{"Use a separate handoff for jobs independent of the new Bud."}
		}
	}
	if len(pending) != 0 {
		if err := snapshotAssignmentWorkspaces(ctx, tx, line.ID); err != nil {
			return Agent{}, nil, "", err
		}
		if err := saveBudProposalWork(ctx, tx, line.ID, pending, ""); err != nil {
			return Agent{}, nil, "", err
		}
	}
	if !draft.CreateNow || !authorizesBudCreation(request, draft.Evidence, agent.Name, profile.Name) {
		if err := addReply(ctx, tx, line.ID, ""); err != nil {
			return Agent{}, nil, "", err
		}
		return Agent{}, nil, "", tx.Commit()
	}

	// Preserve the proposal if the assignment or filesystem prevents creation.
	if _, err := tx.ExecContext(ctx, "SAVEPOINT voice_bud"); err != nil {
		return Agent{}, nil, "", err
	}
	saved, err := s.saveAgentInTx(ctx, tx, profile.ID, profile, true, true)
	if err != nil {
		return Agent{}, nil, "", err
	}
	var jobs []AssignedWork
	var notice string
	mode := CreateBudOnly
	if len(pending) != 0 {
		mode = CreateBudWithWork
		jobs, notice, err = queueBudProposalWork(ctx, tx, line.ID, saved.ID, pending, "")
	}
	if err == nil {
		if _, err := tx.ExecContext(ctx, "INSERT INTO bud_proposal_approvals VALUES (?,?)", line.ID, mode); err != nil {
			return Agent{}, nil, "", err
		}
		if err := addReply(ctx, tx, line.ID, notice); err != nil {
			return Agent{}, nil, "", err
		}
		err = s.commitProposedBud(tx, saved)
		if err == nil {
			return saved, jobs, notice, nil
		}
	}
	var invalid *InputError
	var provision *budProvisionError
	switch {
	case errors.As(err, &invalid):
		notice = "I saved the Bud and its assignments as a draft. " + invalid.Message
	case errors.Is(err, ErrQueueFull):
		notice = "I saved the Bud and its assignments as a draft because the work queue is full."
	case errors.As(err, &provision):
		notice = budProvisionNotice
	default:
		return Agent{}, nil, "", err
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO voice_bud"); err != nil {
		return Agent{}, nil, "", err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE voice_bud"); err != nil {
		return Agent{}, nil, "", err
	}
	if err := addReply(ctx, tx, line.ID, notice); err != nil {
		return Agent{}, nil, "", err
	}
	return Agent{}, nil, notice, tx.Commit()
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
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireOpenConversation(ctx, tx, conversation); err != nil {
		return nil, err
	}
	key := "voice:" + callID + ":" + actionID
	var existing []AssignedWork
	for i := range handoffs {
		var job AssignedWork
		err := tx.QueryRowContext(ctx, `SELECT t.id,t.status,td.title,td.agent_id,ct.agent_name
			FROM tasks t JOIN turn_details td ON td.task_id=t.id JOIN chat_turns ct ON ct.task_id=t.id
			WHERE t.request_key=? OR EXISTS(SELECT 1 FROM assignment_reuses a WHERE a.task_id=t.id AND a.request_key=?)`,
			namespacedKey(BackgroundWork, assignmentKey(key, i)), namespacedKey(BackgroundWork, assignmentKey(key, i))).
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
