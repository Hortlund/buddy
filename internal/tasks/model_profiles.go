package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Worker settings are chosen by the model from the executing host's catalog.
// Ordinary chat stays on the default model without another decision pass.
func (r *CodexRuntime) selectTaskModel(ctx context.Context, turn Turn, emit Emit) (Turn, error) {
	p := turn.Agent.ModelPreferences
	if p != nil && (p.Mode == "fixed" || p.Selected != "" && p.Selected != "automatic") {
		return turn, nil
	}
	if turn.Brief == "" && p != nil && p.Mode == "auto" {
		turn.Agent.ModelPreferences = p.Clone()
		turn.Agent.ModelPreferences.Effort, turn.Agent.ModelPreferences.Speed = "", ""
		return turn, nil
	}
	if turn.Brief != "" && turn.Origin != "peer_reply" && !turn.routing && !turn.revisingBud {
		catalogCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		models, err := r.Models(catalogCtx)
		cancel()
		if ctx.Err() != nil {
			return turn, ctx.Err()
		}
		if err != nil || len(models) == 0 {
			return turn, nil
		}
		return r.selectWorkerModelWith(ctx, turn, models, emit, r.isolatedChatPass)
	}
	return turn, nil
}

func (s *Store) saveTurnModelSelection(ctx context.Context, id string, agent Agent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveTurnModelSelection(ctx, tx, id, agent); err != nil {
		return err
	}
	return tx.Commit()
}

func saveTurnModelSelection(ctx context.Context, tx *sql.Tx, id string, agent Agent) error {
	if _, err := tx.ExecContext(ctx, "UPDATE turn_models SET model=? WHERE task_id=?", agent.Model, id); err != nil {
		return err
	}
	raw, err := json.Marshal(agent.ModelPreferences)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO turn_model_preferences VALUES (?,?) ON CONFLICT(task_id) DO UPDATE SET preferences=excluded.preferences`, id, string(raw)); err != nil {
		return err
	}
	return nil
}

func modelConfigFlags(p *ModelPreferences) []string {
	if p == nil {
		return nil
	}
	var flags []string
	if p.Effort != "" {
		flags = append(flags, "-c", "model_reasoning_effort="+tomlString(p.Effort))
	}
	if p.Speed != "" {
		flags = append(flags, "-c", "service_tier="+tomlString(p.Speed))
	}
	return flags
}
