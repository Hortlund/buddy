package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrSteeringUnavailable = errors.New("This run can't receive steering right now. You can queue the message instead.")

const steeringJSON = `(SELECT json_group_array(json_object('id',s.id,'text',s.text,'state',s.state) ORDER BY s.id)
	FROM steering_messages s WHERE s.task_id=t.id)`

const (
	steerPending = "pending"
	steerApplied = "applied"
	steerMissed  = "missed"
)

type SteeringMessage struct {
	ID    int64  `json:"id"`
	Text  string `json:"text"`
	State string `json:"state"`
}

type ActiveRun struct {
	ID        string       `json:"id"`
	AgentID   string       `json:"agent_id,omitempty"`
	AgentName string       `json:"agent_name,omitempty"`
	Status    Status       `json:"status"`
	Activity  string       `json:"activity"`
	Kind      ActivityKind `json:"activity_kind"`
	Update    string       `json:"update,omitempty"`
	CanSteer  bool         `json:"can_steer"`
}

type turnSteering struct {
	wake  chan struct{}
	ready func(bool)
	next  func(context.Context) (SteeringMessage, error)
	mark  func(int64, string) error
}

func (w *Worker) CanSteer(id string) bool {
	return w.canSteer(id) || (w.background != nil && w.background.canSteer(id))
}

func (w *Worker) canSteer(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[id]
	return run != nil && run.steerReady
}

func (w *Worker) Steer(ctx context.Context, id, text, key string) (SteeringMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 12000 {
		return SteeringMessage{}, &InputError{"Write a message between 1 and 12,000 bytes."}
	}
	lane, err := w.lane(ctx, id)
	if err != nil {
		return SteeringMessage{}, err
	}
	return lane.steerTask(ctx, id, text, key)
}

func (w *Worker) steerTask(ctx context.Context, id, text, key string) (SteeringMessage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[id]
	item, created, err := w.store.queueSteering(ctx, id, text, key, run != nil && run.steerReady)
	if err == nil && created && run != nil {
		select {
		case run.steerWake <- struct{}{}:
		default:
		}
	}
	return item, err
}

func (s *Store) queueSteering(ctx context.Context, id, text, key string, available bool) (SteeringMessage, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SteeringMessage{}, false, err
	}
	defer tx.Rollback()
	if key == "" {
		key = rand.Text()
	}
	var existing SteeringMessage
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT id,task_id,text,state FROM steering_messages WHERE request_key=?`, key).
		Scan(&existing.ID, &owner, &existing.Text, &existing.State)
	if err == nil {
		if owner != id || existing.Text != text {
			return SteeringMessage{}, false, ErrConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SteeringMessage{}, false, err
	}
	if !available {
		return SteeringMessage{}, false, ErrSteeringUnavailable
	}
	if name, _ := SkillCommand(text); name != "" {
		instructions, err := skillInstructions(ctx, tx, name)
		if err != nil {
			return SteeringMessage{}, false, err
		}
		if instructions != "" {
			return SteeringMessage{}, false, &InputError{"Send this skill as a follow-up to run it in a new turn."}
		}
	}
	var status Status
	err = tx.QueryRowContext(ctx, "SELECT status FROM tasks WHERE id=? AND kind IN (?,?)", id, AgentTurn, BackgroundWork).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != Running) {
		return SteeringMessage{}, false, ErrSteeringUnavailable
	}
	if err != nil {
		return SteeringMessage{}, false, err
	}
	var count, size int
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(length(CAST(text AS BLOB))),0)
		FROM steering_messages WHERE task_id=?`, id).Scan(&count, &size)
	if err != nil {
		return SteeringMessage{}, false, err
	}
	if count >= 16 || size+len(text) > 24000 {
		return SteeringMessage{}, false, &InputError{"This run has reached its steering limit. Queue the message to follow up."}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO steering_messages(task_id,request_key,text,state)
		VALUES (?,?,?,?)`, id, key, text, steerPending)
	if err != nil {
		return SteeringMessage{}, false, err
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		return SteeringMessage{}, false, err
	}
	return SteeringMessage{ID: messageID, Text: text, State: steerPending}, true, tx.Commit()
}

func (s *Store) nextSteering(ctx context.Context, id string) (SteeringMessage, error) {
	var item SteeringMessage
	err := s.db.QueryRowContext(ctx, `SELECT id,text,state FROM steering_messages
		WHERE task_id=? AND state=? ORDER BY id LIMIT 1`, id, steerPending).
		Scan(&item.ID, &item.Text, &item.State)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return item, err
}

func (s *Store) markSteering(id int64, state string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, "UPDATE steering_messages SET state=? WHERE id=? AND state=?", state, id, steerPending)
	return err
}
