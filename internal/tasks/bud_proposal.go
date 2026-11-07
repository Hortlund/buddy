package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
)

type BudDraft struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	Instructions string `json:"instructions"`
}

type BudProposal struct {
	BudDraft
	ConversationID string `json:"conversation_id,omitempty"`
}

func saveBudProposal(ctx context.Context, tx *sql.Tx, id string, draft BudDraft) error {
	draft.Name, draft.Role = strings.TrimSpace(draft.Name), strings.TrimSpace(draft.Role)
	draft.Instructions = strings.TrimSpace(draft.Instructions)
	if !validTitle(draft.Name, 100) || !validTitle(draft.Role, 100) || draft.Instructions == "" || len(draft.Instructions) > 8000 || strings.ContainsRune(draft.Instructions, 0) {
		return &InputError{"A Bud draft needs a short name and role, and instructions under 8,000 bytes."}
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO bud_proposals VALUES (?,?,?,?,?)",
		id, "agt_"+rand.Text(), draft.Name, draft.Role, draft.Instructions)
	return err
}

func (s *Store) CreateProposedBud(ctx context.Context, chat, task string) (Agent, error) {
	var id string
	var profile Agent
	err := s.db.QueryRowContext(ctx, `SELECT p.bud_id,p.name,p.role,p.instructions FROM bud_proposals p
		JOIN chat_turns ct ON ct.task_id=p.task_id WHERE p.task_id=? AND ct.conversation_id=?`, task, chat).
		Scan(&id, &profile.Name, &profile.Role, &profile.Instructions)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	if saved, err := s.Agent(ctx, id); !errors.Is(err, ErrNotFound) {
		saved.ConversationID = "conv_" + strings.TrimPrefix(id, "agt_")
		return saved, err
	}
	saved, err := s.saveAgent(ctx, id, profile, true)
	if err != nil {
		// A duplicate click or a request retried after a lost response uses
		// the same Bud ID. Creation and its conversation commit together.
		if existing, lookupErr := s.Agent(ctx, id); lookupErr == nil {
			existing.ConversationID = "conv_" + strings.TrimPrefix(id, "agt_")
			return existing, nil
		}
	}
	return saved, err
}
