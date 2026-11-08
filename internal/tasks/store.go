package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound       = errors.New("task not found")
	ErrConflict       = errors.New("request ID was already used for a different task")
	ErrInactive       = errors.New("task is already finished")
	ErrQueueFull      = errors.New("task queue is full")
	ErrNoLongerQueued = errors.New("this request is no longer queued")
)

type Store struct {
	db            *sql.DB
	dbPath        string
	workspaceRoot string
	profilesRoot  string
	startedAt     time.Time
}

const fields = "id, kind, repo, command, timeout, status, summary, created, started, finished"

const taskTable = ` (
	id TEXT PRIMARY KEY,
	request_key TEXT NOT NULL UNIQUE,
	kind TEXT NOT NULL,
	repo TEXT NOT NULL,
	command TEXT NOT NULL,
	timeout INTEGER NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('queued','running','needs_input','cancelling','succeeded','failed','cancelled','interrupted')),
	summary TEXT NOT NULL,
	created INTEGER NOT NULL,
	started INTEGER,
	finished INTEGER
);`

const schema = "CREATE TABLE IF NOT EXISTS tasks" + taskTable + `
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status);
CREATE TABLE IF NOT EXISTS events (
	seq INTEGER PRIMARY KEY,
	task_id TEXT NOT NULL REFERENCES tasks(id),
	at INTEGER NOT NULL,
	stream TEXT NOT NULL,
	text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_task_seq ON events(task_id, seq);
`

func OpenStore(ctx context.Context, path string) (*Store, error) {
	// Create with private permissions before SQLite opens it or creates its WAL.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=3000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema+chatSchema+workspaceSchema+cardsSchema+deviceSchema+attachmentSchema+voiceSchema+voiceTeamSchema+preferencesSchema+workQuestionsSchema+workInputSchema+standingSchema+skillSchema+removedBudSchema+connectionSchema+mcpSchema+assignmentSchema+managementSchema+historySchema+budMemorySchema+reactionSchema+githubWriteSchema+teamSchema+executionSchema+workControlSchema+automationStateSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	if err := migrateAvatarShape(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade avatar settings: %w", err)
	}
	if err := migrateWorkInputStatus(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade background job statuses: %w", err)
	}
	if err := migrateAssignmentParents(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade job assignments: %w", err)
	}
	if err := migrateWatchFilters(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade watch filters: %w", err)
	}
	if err := migrateMCPServers(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade MCP settings: %w", err)
	}
	if _, err := db.ExecContext(ctx, fileReferenceSchema+mobileSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize file references and iPhone access: %w", err)
	}
	return &Store{db: db, dbPath: path, startedAt: time.Now().UTC()}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (Task, error) {
	var t Task
	var args string
	var created int64
	var started, finished sql.NullInt64
	if err := row.Scan(&t.ID, &t.Kind, &t.Repository, &args, &t.TimeoutSeconds,
		&t.Status, &t.Summary, &created, &started, &finished); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t, ErrNotFound
		}
		return t, err
	}
	if err := json.Unmarshal([]byte(args), &t.Command); err != nil {
		return t, err
	}
	t.CreatedAt = time.UnixMicro(created).UTC()
	if started.Valid {
		x := time.UnixMicro(started.Int64).UTC()
		t.StartedAt = &x
	}
	if finished.Valid {
		x := time.UnixMicro(finished.Int64).UTC()
		t.FinishedAt = &x
	}
	return t, nil
}

// HTTP header values cannot contain NUL, so a namespaced key cannot
// collide with any key previously stored verbatim.
func namespacedKey(kind, key string) string { return kind + "\x00" + key }

func (s *Store) Create(ctx context.Context, req Request, key string) (Task, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, err
	}
	defer tx.Rollback()
	if key != "" {
		t, err := scanTask(tx.QueryRowContext(ctx,
			"SELECT "+fields+" FROM tasks WHERE kind = ? AND request_key IN (?, ?)", req.Kind, namespacedKey(req.Kind, key), key))
		if err == nil {
			if !t.Request.equal(req) {
				return Task{}, false, ErrConflict
			}
			return t, false, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Task{}, false, err
		}
	} else {
		key = rand.Text()
	}
	if err := checkQueue(ctx, tx, false); err != nil {
		return Task{}, false, err
	}
	t := Task{
		ID: "tsk_" + rand.Text(), Request: req, Status: Queued,
		Summary: "Waiting to start.", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	args, err := json.Marshal(req.Command)
	if err != nil {
		return Task{}, false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks (id, request_key, kind, repo, command, timeout, status, summary, created)
		VALUES (?,?,?,?,?,?,?,?,?)`, t.ID, namespacedKey(t.Kind, key), t.Kind, t.Repository, string(args), t.TimeoutSeconds,
		t.Status, t.Summary, t.CreatedAt.UnixMicro())
	if err != nil {
		return Task{}, false, err
	}
	if err := addEvent(ctx, tx, t.ID, "system", "Task queued.\n"); err != nil {
		return Task{}, false, err
	}
	return t, true, tx.Commit()
}

func (s *Store) List(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+fields+` FROM tasks
		WHERE kind <> 'agent_turn' OR EXISTS (
			SELECT 1 FROM events WHERE task_id = tasks.id AND stream = 'activity'
		) ORDER BY rowid DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, "SELECT "+fields+" FROM tasks WHERE id = ?", id))
}

func (s *Store) claim(ctx context.Context) (Task, error) {
	return s.claimLane(ctx, false)
}

func (s *Store) claimLane(ctx context.Context, background bool, occupied ...string) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT t.id,t.repo FROM tasks t WHERE t.status='queued' AND (t.kind='background_work')=?
			AND NOT EXISTS(SELECT 1 FROM device_runs dr WHERE dr.task_id=t.id)
			AND NOT EXISTS(SELECT 1 FROM pooled_work p WHERE p.task_id=t.id)
			AND NOT EXISTS(SELECT 1 FROM restored_workspace_tasks h WHERE h.task_id=t.id)
			AND NOT EXISTS(SELECT 1 FROM voice_team_steps step JOIN voice_team_runs run ON run.call_id=step.call_id
				LEFT JOIN tasks previous ON previous.id=step.previous_task
				WHERE step.task_id=t.id AND (run.released_at=0 OR previous.status IN ('queued','running','needs_input','cancelling')))
			AND `+dependenciesReady+`
			ORDER BY EXISTS(SELECT 1 FROM standing_inbox i WHERE i.task_id=t.id),t.rowid`, background)
	if err != nil {
		return Task{}, err
	}
	type queuedCandidate struct{ id, repo string }
	var candidates []queuedCandidate
	for rows.Next() {
		var candidate queuedCandidate
		if err := rows.Scan(&candidate.id, &candidate.repo); err != nil {
			rows.Close()
			return Task{}, err
		}
		candidates = append(candidates, candidate)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Task{}, err
	}
	var id string
	for _, candidate := range candidates {
		plan, err := readExecutionPlan(ctx, tx, candidate.id)
		if err != nil {
			return Task{}, err
		}
		workspace, reason, err := executionWorkspace(ctx, tx, "server", plan)
		if err != nil {
			return Task{}, err
		}
		if reason != "" {
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET summary=? WHERE id=?", reason, candidate.id); err != nil {
				return Task{}, err
			}
			continue
		}
		if plan.ProjectID != "" && workspace != candidate.repo {
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET summary=? WHERE id=?", "The project's checkout changed. Stop this assignment and start it again for the new checkout.", candidate.id); err != nil {
				return Task{}, err
			}
			continue
		}
		if slices.Contains(plan.Requirements, "desktop") {
			var busy bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_execution e JOIN tasks t ON t.id=e.task_id
                WHERE t.status IN ('running','cancelling') AND NOT EXISTS(SELECT 1 FROM device_runs dr WHERE dr.task_id=t.id)
                AND EXISTS(SELECT 1 FROM json_each(e.requirements) WHERE value='desktop'))`).Scan(&busy); err != nil {
				return Task{}, err
			}
			if busy {
				continue
			}
		}
		_, gitErr := os.Stat(filepath.Join(candidate.repo, ".git"))
		isolatedProject := plan.ProjectID != "" && gitErr == nil
		if isolatedProject || candidate.repo == "" || !workspaceOccupied(candidate.repo, occupied) {
			id = candidate.id
			break
		}
	}
	if id == "" {
		if err := tx.Commit(); err != nil {
			return Task{}, err
		}
		return Task{}, ErrNotFound
	}
	t, err := scanTask(tx.QueryRowContext(ctx, `
		UPDATE tasks SET status='running',started=?,summary=CASE kind WHEN 'repo_check' THEN 'Running repository checks.' ELSE ? END
		WHERE id=? RETURNING `+fields, time.Now().UnixMicro(), activityLabels[Starting], id))
	if err != nil {
		return Task{}, err
	}
	if t.Kind == RepoCheck {
		if err := addEvent(ctx, tx, t.ID, "system", "Started repository check.\n"); err != nil {
			return Task{}, err
		}
	}
	if t.Kind == BackgroundWork {
		if _, err := tx.ExecContext(ctx, "UPDATE standing_inbox SET attempts=attempts+1 WHERE task_id=?", t.ID); err != nil {
			return Task{}, err
		}
	}
	return t, tx.Commit()
}

func workspaceOccupied(repo string, occupied []string) bool {
	for _, other := range occupied {
		overlap, err := WorkspacePathsOverlap(repo, other)
		if overlap || err != nil {
			return true
		}
	}
	return false
}

func (s *Store) cancel(ctx context.Context, id string, queuedOnly bool) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	t, err := cancelTask(ctx, tx, id, queuedOnly)
	if err != nil {
		return t, err
	}
	return t, tx.Commit()
}

func cancelTask(ctx context.Context, tx *sql.Tx, id string, queuedOnly bool) (Task, error) {
	t, err := cancelSingleTask(ctx, tx, id, queuedOnly)
	if err != nil {
		return t, err
	}
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE children(id) AS (
		SELECT task_id FROM turn_details WHERE parent_id=? UNION
		SELECT d.task_id FROM turn_details d JOIN children c ON d.parent_id=c.id)
		SELECT t.id FROM tasks t JOIN children c ON c.id=t.id WHERE t.status IN ('queued','running','needs_input')`, id)
	if err != nil {
		return t, err
	}
	var children []string
	for rows.Next() {
		var child string
		if err = rows.Scan(&child); err != nil {
			rows.Close()
			return t, err
		}
		children = append(children, child)
	}
	rowErr := rows.Err()
	if err = rows.Close(); err != nil {
		return t, err
	}
	if rowErr != nil {
		return t, rowErr
	}
	for _, child := range children {
		if _, err = cancelSingleTask(ctx, tx, child, false); err != nil {
			return t, err
		}
	}
	return t, nil
}

func cancelSingleTask(ctx context.Context, tx *sql.Tx, id string, queuedOnly bool) (Task, error) {
	t, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+fields+" FROM tasks WHERE id = ?", id))
	if err != nil {
		return t, err
	}
	if queuedOnly && t.Status != Queued {
		return t, ErrNoLongerQueued
	}
	if t.Kind == "work_input" {
		return t, ErrInactive
	}
	wasWaiting := t.Status == NeedsInput
	switch t.Status {
	case Queued, NeedsInput:
		t.Status, t.Summary = Cancelled, "Cancelled before starting."
		if wasWaiting {
			t.Summary = "Cancelled while waiting for input."
		}
		if err := closeWorkInput(ctx, tx, id, Cancelled); err != nil {
			return t, err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		t.FinishedAt = &now
		_, err = tx.ExecContext(ctx, "UPDATE tasks SET status=?, summary=?, finished=? WHERE id=?",
			t.Status, t.Summary, now.UnixMicro(), id)
	case Running:
		t.Status, t.Summary = Cancelling, "Stopping…"
		_, err = tx.ExecContext(ctx, "UPDATE tasks SET status=?, summary=? WHERE id=?", t.Status, t.Summary, id)
	case Cancelling:
		return t, nil
	default:
		return t, ErrInactive
	}
	if err != nil {
		return t, err
	}
	if (t.Kind == AgentTurn || t.Kind == BackgroundWork) && t.Status == Cancelled {
		reply := "I cancelled this request before starting it."
		if wasWaiting {
			reply = "I stopped waiting for input and cancelled the job."
		}
		if err := addReply(ctx, tx, id, reply); err != nil {
			return t, err
		}
	}
	if err := addEvent(ctx, tx, id, "system", t.Summary+"\n"); err != nil {
		return t, err
	}
	return t, nil
}

func (s *Store) finish(ctx context.Context, id string, status Status, summary, reply string) error {
	return s.finishTurn(ctx, id, status, summary, reply, nil)
}

func (s *Store) finishTurn(ctx context.Context, id string, status Status, summary, reply string, action *TurnAction) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := finishTask(ctx, tx, id, status, summary, reply, action); err != nil {
		return err
	}
	return tx.Commit()
}

func finishTask(ctx context.Context, tx *sql.Tx, id string, status Status, summary, reply string, action *TurnAction) error {
	var current Status
	var kind string
	if err := tx.QueryRowContext(ctx, "SELECT status,kind FROM tasks WHERE id=?", id).Scan(&current, &kind); err != nil {
		return err
	}
	if current == Cancelling {
		status, summary = Cancelled, "Repository check cancelled."
		if kind == AgentTurn || kind == BackgroundWork {
			summary, reply = "Cancelled.", "I stopped working on this request."
		}
	}
	if current != Running && current != Cancelling {
		return ErrInactive
	}
	var finished any
	if status != NeedsInput {
		finished = time.Now().UnixMicro()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status=?, summary=?, finished=? WHERE id=?",
		status, summary, finished, id); err != nil {
		return err
	}
	if kind == BackgroundWork {
		if err := addWatchReceipt(ctx, tx, id, status); err != nil {
			return err
		}
	}
	if kind == AgentTurn || kind == BackgroundWork {
		if _, err := tx.ExecContext(ctx, "UPDATE steering_messages SET state=? WHERE task_id=? AND state=?", steerMissed, id, steerPending); err != nil {
			return err
		}
		if status == NeedsInput && kind == BackgroundWork {
			return requestWorkInput(ctx, tx, id, reply)
		}
		if status == Succeeded && kind == AgentTurn && action != nil {
			if !action.hasManagement() {
				if err := saveLearning(ctx, tx, id, action.Learning); err != nil {
					return err
				}
			}
			note, err := saveTurnAction(ctx, tx, id, action, reply)
			if err != nil {
				return err
			}
			if action.hasHandoff() || action.hasRoutines() || action.hasManagement() {
				// Only acknowledge changes that were actually saved.
				reply = note
			} else if note != "" {
				if reply != "" {
					reply += "\n\n"
				}
				reply += note
			}
		}
		if status == Succeeded && action != nil && action.reactionSaved && reply == "" {
			return addEvent(ctx, tx, id, "system", "\nReacted.\n")
		}
		if reply == "" && (!action.hasCard() || status != Succeeded) {
			reply = summary
		}
		if err := addReply(ctx, tx, id, reply); err != nil {
			return err
		}
	}
	return addEvent(ctx, tx, id, "system", "\n"+summary+"\n")
}

// Recover preserves queued work and records any uncertain in-flight outcome.
// An interrupted command can have side effects, so it is never replayed here.
func (s *Store) Recover(ctx context.Context) error {
	const message = "The server stopped before this check finished. Run it again to retry."
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMicro()
	if _, err := requeueStandingWork(ctx, tx, ""); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE standing_inbox SET seen=0 WHERE task_id IN
		(SELECT id FROM tasks WHERE status IN ('running','cancelling'))`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO standing_delivery(id,generation,due)
		SELECT i.id,'interrupted:' || i.attempts,? FROM standing_inbox i JOIN tasks t ON t.id=i.task_id
		WHERE t.status IN ('running','cancelling')
		ON CONFLICT(id) DO UPDATE SET generation=excluded.generation,due=excluded.due,attempts=0,sent=0`, now); err != nil {
		return err
	}
	// No voice connection survives a server restart.
	if _, err := tx.ExecContext(ctx, "UPDATE voice_team_runs SET released_at=? WHERE released_at=0", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE device_runs SET lease='',expires=0
		WHERE task_id IN (SELECT id FROM tasks WHERE status='queued')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events(task_id,at,stream,text)
		SELECT id,?,'system',? FROM tasks WHERE status IN ('running','cancelling')`, now, "\n"+message+"\n"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO messages(task_id,role,text,created)
		SELECT ct.task_id,'assistant',CASE WHEN EXISTS(SELECT 1 FROM choice_cards q
            JOIN github_write_cards p ON p.task_id=q.task_id WHERE q.answer_task_id=ct.task_id AND q.choice=0)
        THEN ? ELSE 'I was interrupted when the server stopped. Send the request again to retry.' END,?
		FROM chat_turns ct JOIN tasks t ON t.id=ct.task_id WHERE t.status IN ('running','cancelling')`, GitHubWriteUncertain, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE steering_messages SET state=? WHERE state=?", steerMissed, steerPending); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tasks SET status='interrupted', summary=?, finished=?
		WHERE status IN ('running','cancelling')`, message, now); err != nil {
		return err
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func addEvent(ctx context.Context, db execer, id, stream, content string) error {
	_, err := db.ExecContext(ctx, "INSERT INTO events(task_id,at,stream,text) VALUES (?,?,?,?)",
		id, time.Now().UnixMicro(), stream, content)
	return err
}

func (s *Store) append(ctx context.Context, id, stream, content string) error {
	return addEvent(ctx, s.db, id, stream, content)
}

func (s *Store) Events(ctx context.Context, id string, after int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, at, stream, text FROM events WHERE task_id = ? AND seq > ?
		ORDER BY seq LIMIT 256`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Event, 0)
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&e.Seq, &at, &e.Stream, &e.Text); err != nil {
			return nil, err
		}
		e.At = time.UnixMicro(at).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
