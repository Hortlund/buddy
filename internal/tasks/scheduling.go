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

type schedulePermissions struct {
	Jobs         []int             `json:"jobs"`
	Routines     []int             `json:"routines"`
	RoutineEdits []int             `json:"routine_edits"`
	Automation   []automationGrant `json:"automation"`
}

type automationGrant struct {
	Job        int    `json:"job"`
	Inspection string `json:"inspection_scope"`
	Write      string `json:"write_scope"`
}

const maxAutomationWriteScope = 4096

func (plan ExecutionPlan) usesAutomation() bool {
	return slices.Contains(plan.Requirements, "browser") || slices.Contains(plan.Requirements, "desktop")
}

// HandoffNeedsAuthorization covers both workspace writes and interactive tools.
func HandoffNeedsAuthorization(job Handoff) bool {
	return !job.ReadOnly || job.ExecutionPlan.usesAutomation()
}

func (a *TurnAction) authorizeSchedule(ctx context.Context, authorize func(context.Context, []Handoff, []RoutineDraft) schedulePermissions) {
	// Discard ambiguous actions while preserving unrelated work.
	if a.Handoff != nil && len(a.Handoffs) != 0 {
		a.Handoff, a.Handoffs = nil, nil
		a.scheduleNotice = "I skipped the jobs because the assignment list was ambiguous."
	}
	if a.Routine != nil && len(a.Routines) != 0 {
		a.Routine, a.Routines = nil, nil
		a.scheduleNotice = joinActionNotes(a.scheduleNotice, "I skipped the routines because the schedule was ambiguous.")
	}
	jobs := slices.Clone(a.Handoffs)
	if a.Handoff != nil && len(jobs) == 0 {
		jobs = []Handoff{*a.Handoff}
	}
	routines := a.Routines
	if a.Routine != nil && len(routines) == 0 {
		routines = []RoutineDraft{*a.Routine}
	}
	for i := range routines {
		routines[i].AllowEdits = false
		routines[i].ResearchPosition = 0
	}
	var protected []Handoff
	jobPositions := make([]int, len(jobs))
	for i, job := range jobs {
		job.AutomationWriteScope = ""
		job.AutomationInspectionScope = ""
		jobs[i] = job
		if HandoffNeedsAuthorization(job) {
			protected = append(protected, job)
			jobPositions[i] = len(protected)
		}
	}
	if len(protected) == 0 && len(routines) == 0 {
		if a.Handoff != nil {
			a.Handoff = &jobs[0]
		} else {
			a.Handoffs = jobs
		}
		return
	}
	permissions := authorize(ctx, protected, routines)
	grants := make(map[int]automationGrant)
	seenGrants := make(map[int]bool)
	for _, grant := range permissions.Automation {
		if seenGrants[grant.Job] {
			delete(grants, grant.Job)
			continue
		}
		seenGrants[grant.Job] = true
		grant.Inspection = strings.TrimSpace(grant.Inspection)
		grant.Write = strings.TrimSpace(grant.Write)
		if grant.Inspection != "" && len(grant.Inspection) <= maxAutomationWriteScope && len(grant.Write) <= maxAutomationWriteScope {
			grants[grant.Job] = grant
		}
	}
	keptJobs := make([]Handoff, 0, len(jobs))
	keptPositions := make([]int, len(jobs)+1)
	for i, job := range jobs {
		if jobPositions[i] != 0 && !slices.Contains(permissions.Jobs, jobPositions[i]) {
			continue
		}
		if job.usesAutomation() {
			grant, ok := grants[jobPositions[i]]
			if !ok {
				continue
			}
			job.AutomationInspectionScope, job.AutomationWriteScope = grant.Inspection, grant.Write
		}
		after := slices.Clone(job.After)
		blocked := false
		for j, dependency := range job.After {
			if dependency < 1 || dependency > i {
				// Leave malformed references for the existing schedule validator.
				continue
			}
			if keptPositions[dependency] == 0 {
				blocked = true
				break
			}
			after[j] = keptPositions[dependency]
		}
		if blocked {
			continue
		}
		keptPositions[i+1] = len(keptJobs) + 1
		job.After = after
		keptJobs = append(keptJobs, job)
	}
	if a.Handoff != nil && len(a.Handoffs) == 0 {
		a.Handoff = nil
		if len(keptJobs) != 0 {
			a.Handoff = &keptJobs[0]
		}
	} else {
		a.Handoffs = keptJobs
	}
	keptRoutines := make([]RoutineDraft, 0, len(routines))
	for i, draft := range routines {
		if slices.Contains(permissions.Routines, i+1) {
			draft.AllowEdits = slices.Contains(permissions.RoutineEdits, i+1)
			draft.ResearchPosition = i + 1
			keptRoutines = append(keptRoutines, draft)
		}
	}
	if a.Routine != nil && len(a.Routines) == 0 {
		a.Routine = nil
		if len(keptRoutines) != 0 {
			a.Routine = &keptRoutines[0]
		}
	} else {
		a.Routines = keptRoutines
	}
	if len(keptJobs) != len(jobs) || len(keptRoutines) != len(routines) {
		a.scheduleNotice = joinActionNotes(a.scheduleNotice, "I skipped work that needed authorization or depended on it.")
	}
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
	for i, draft := range b.Routines {
		position := draft.ResearchPosition
		if position == 0 {
			position = i + 1
		}
		note, err := saveRequestedRoutine(ctx, tx, b.Source, draft, position)
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
