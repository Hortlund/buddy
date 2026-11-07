package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type turnSchedule struct {
	Source             string         `json:"-"`
	Routines           []RoutineDraft `json:"routines,omitempty"`
	Handoffs           []Handoff      `json:"handoffs,omitempty"`
	AllowOutside       bool           `json:"allow_outside,omitempty"`
	EarlierAttachments string         `json:"earlier_attachments,omitempty"`
}

func (a *TurnAction) schedule(task string) (turnSchedule, error) {
	batch := turnSchedule{Source: task, Routines: slices.Clone(a.Routines), Handoffs: a.Handoffs,
		AllowOutside: a.Handoff == nil, EarlierAttachments: a.earlierAttachmentSource}
	if a.Routine != nil {
		if len(batch.Routines) != 0 {
			return turnSchedule{}, &InputError{"A routine was supplied in two different formats."}
		}
		batch.Routines = []RoutineDraft{*a.Routine}
	}
	if a.Handoff != nil {
		if len(batch.Handoffs) != 0 {
			return turnSchedule{}, &InputError{"A background job was supplied in two different formats."}
		}
		batch.Handoffs = []Handoff{*a.Handoff}
	}
	if len(batch.Routines)+len(batch.Handoffs) > 8 {
		return turnSchedule{}, &InputError{"Schedule up to eight routines and jobs in a single request."}
	}
	for i := range batch.Routines {
		draft := &batch.Routines[i]
		draft.Name, draft.Prompt = strings.TrimSpace(draft.Name), strings.TrimSpace(draft.Prompt)
		if err := validateRoutine(draft.Name, draft.Prompt, draft.Minutes); err != nil {
			return turnSchedule{}, err
		}
	}
	return batch, nil
}

func (b turnSchedule) preview() []string {
	lines := make([]string, 0, len(b.Routines)+len(b.Handoffs))
	for _, routine := range b.Routines {
		lines = append(lines, fmt.Sprintf("Schedule routine “%s” · %s", routine.Name, RoutineCadence(routine.Minutes)))
	}
	for _, job := range b.Handoffs {
		lines = append(lines, "Start background job “"+managementExcerpt(job.Title, 120)+"”")
	}
	return lines
}

func (b turnSchedule) apply(ctx context.Context, tx *sql.Tx) (string, error) {
	notes := make([]string, 0, len(b.Routines)+1)
	for _, draft := range b.Routines {
		note, err := saveRequestedRoutine(ctx, tx, b.Source, draft)
		if err != nil {
			return "", err
		}
		notes = append(notes, note)
	}
	if len(b.Handoffs) != 0 {
		note, err := saveHandoffs(ctx, tx, b.Source, b.Handoffs, b.EarlierAttachments, b.AllowOutside)
		if err != nil {
			return "", err
		}
		notes = append(notes, note)
	}
	return strings.Join(notes, "\n\n"), nil
}

func saveScheduledTurn(ctx context.Context, tx *sql.Tx, task string, action *TurnAction, reply string) (string, error) {
	batch, err := action.schedule(task)
	var note string
	if err == nil {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT schedule_batch"); err != nil {
			return "", err
		}
		note, err = batch.apply(ctx, tx)
		if err != nil {
			if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO schedule_batch"); rollbackErr != nil {
				return "", rollbackErr
			}
		}
		if _, releaseErr := tx.ExecContext(ctx, "RELEASE schedule_batch"); releaseErr != nil {
			return "", releaseErr
		}
	}
	var invalid *InputError
	switch {
	case errors.Is(err, ErrQueueFull):
		return "I couldn’t schedule that batch. The background work queue is full.", nil
	case errors.As(err, &invalid):
		if !action.hasHandoff() && len(action.Routines) <= 1 {
			return "I couldn't schedule the routine. " + invalid.Message, nil
		}
		return "I couldn’t schedule that batch. " + invalid.Message, nil
	case err != nil:
		return "", err
	}
	if reply != "" {
		note = reply + "\n\n" + note
	}
	return note, nil
}
