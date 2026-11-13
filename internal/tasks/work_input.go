package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const workInputSchema = `
CREATE TABLE IF NOT EXISTS work_inputs (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	job_id TEXT NOT NULL REFERENCES tasks(id),
	answer_key TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS work_inputs_pending ON work_inputs(job_id) WHERE answer_key IS NULL;
CREATE INDEX IF NOT EXISTS work_inputs_job ON work_inputs(job_id);
`

const workInputRounds = 8

var ErrWorkNeedsInput = errors.New("background job needs input")

func migrateWorkInputStatus(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var definition string
	if err := conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE name='tasks' AND type='table'").Scan(&definition); err != nil {
		return err
	}
	if strings.Contains(definition, "'needs_input'") {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE tasks_upgrade"+taskTable+`
		INSERT INTO tasks_upgrade(rowid,id,request_key,kind,repo,command,timeout,status,summary,created,started,finished)
		SELECT rowid,id,request_key,kind,repo,command,timeout,status,summary,created,started,finished FROM tasks;
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE tasks;
		ALTER TABLE tasks_upgrade RENAME TO tasks;
		CREATE INDEX tasks_status ON tasks(status);`); err != nil {
		return err
	}
	var orphan string
	if err := tx.QueryRowContext(ctx, "SELECT \"table\" FROM pragma_foreign_key_check LIMIT 1").Scan(&orphan); err == nil {
		return errors.New("task status upgrade found a broken reference in " + orphan)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	return err
}

func requestWorkInput(ctx context.Context, tx *sql.Tx, job, question string) error {
	var rounds int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM work_inputs WHERE job_id=?", job).Scan(&rounds); err != nil {
		return err
	}
	if rounds >= workInputRounds {
		const summary = "Couldn't finish without more input."
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status='failed',summary=?,finished=? WHERE id=?", summary, time.Now().UnixMicro(), job); err != nil {
			return err
		}
		return addReply(ctx, tx, job, question+"\n\nI’ve reached this job’s follow-up limit. Send a new request with the missing details to continue.")
	}
	id, now := "tsk_"+rand.Text(), time.Now().UnixMicro()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,request_key,kind,repo,command,timeout,status,summary,created)
		VALUES (?,?,'work_input','','null',0,'needs_input','Waiting for your reply.',?)`, id, id, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_turns
		SELECT ?,conversation_id,agent_name,instructions,allow_edits FROM chat_turns WHERE task_id=?`, id, job); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO turn_details
		SELECT ?,agent_id,agent_role,'work_input',title,'',NULL,NULL FROM turn_details WHERE task_id=?`, id, job); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO work_inputs(task_id,job_id) VALUES (?,?)", id, job); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO work_questions VALUES (?,?)", id, job); err != nil {
		return err
	}
	if err := addReply(ctx, tx, id, question); err != nil {
		return err
	}
	return addEvent(ctx, tx, job, "system", "Asked for input in chat.")
}

func closeWorkInput(ctx context.Context, tx *sql.Tx, job string, status Status) error {
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,finished=? WHERE status='needs_input' AND id IN
		(SELECT task_id FROM work_inputs WHERE job_id=? AND answer_key IS NULL)`, status, time.Now().UnixMicro(), job)
	return err
}

func (s *Store) AnswerWorkInput(ctx context.Context, chat, input, text, key string, files []Attachment) (bool, error) {
	text = strings.TrimSpace(text)
	if (text == "" && len(files) == 0) || len(text) > 12000 {
		return false, &InputError{"Write a reply or attach a file. Replies can be up to 12,000 bytes."}
	}
	files, err := PrepareAttachments(files)
	if err != nil {
		return false, err
	}
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var job, parent, origin string
	var status Status
	var answered sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT w.job_id,COALESCE(td.parent_id,''),td.origin,t.status,w.answer_key
		FROM work_inputs w JOIN tasks t ON t.id=w.job_id JOIN turn_details td ON td.task_id=w.job_id
		JOIN chat_turns ct ON ct.task_id=w.job_id WHERE w.task_id=? AND ct.conversation_id=?`, input, chat).
		Scan(&job, &parent, &origin, &status, &answered)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if answered.Valid {
		if key == "" || key != answered.String {
			return false, ErrConflict
		}
		var savedText string
		if err := tx.QueryRowContext(ctx, "SELECT text FROM messages WHERE task_id=? AND role='user'", input).Scan(&savedText); err != nil {
			return false, err
		}
		saved, err := readAttachments(ctx, tx, input)
		if err != nil {
			return false, err
		}
		if text != savedText || AttachmentDigest(files) != AttachmentDigest(saved) {
			return false, ErrConflict
		}
		return false, nil
	}
	if err := requireOpenConversation(ctx, tx, chat); err != nil {
		return false, err
	}
	if status != NeedsInput {
		return false, ErrInactive
	}
	if origin == WatchOrigin && len(files) != 0 {
		return false, &InputError{"Reply to this watch with text. You can paste the relevant details into your reply."}
	}
	if err := checkQueue(ctx, tx, true); err != nil {
		return false, err
	}
	source, err := handoffAttachmentSource(ctx, tx, job, parent)
	if err != nil {
		return false, err
	}
	existing, err := readWorkAttachments(ctx, tx, job, source)
	if err != nil {
		return false, err
	}
	if _, err := PrepareAttachments(append(existing, files...)); err != nil {
		return false, &InputError{"The job can carry up to five files totaling 8 MB, including files it already has. Send a text reply or fewer files."}
	}
	if key == "" {
		key = rand.Text()
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages(task_id,role,text,created) VALUES (?,'user',?,?)", input, text, time.Now().UnixMicro()); err != nil {
		return false, err
	}
	for i, file := range files {
		if _, err := tx.ExecContext(ctx, "INSERT INTO chat_attachments VALUES (?,?,?,?,COALESCE(?,X''))", input, i, file.Name, file.Type, file.Data); err != nil {
			return false, err
		}
	}
	if err := closeWorkInput(ctx, tx, job, Succeeded); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE work_inputs SET answer_key=? WHERE task_id=?", key, input); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='queued',finished=NULL,started=NULL,
		summary=CASE WHEN EXISTS(SELECT 1 FROM restored_workspace_tasks WHERE task_id=tasks.id) THEN ? ELSE 'Waiting to resume.' END
		WHERE id=?`, WorkspaceReviewMessage, job); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE device_runs SET lease='',expires=0,reported=0 WHERE task_id=?", job); err != nil {
		return false, err
	}
	if err := addEvent(ctx, tx, job, "system", "Received your reply and will resume."); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func readWorkAttachments(ctx context.Context, db queryer, job, source string) ([]Attachment, error) {
	return queryAttachments(ctx, db, requestAttachmentChain+`
		SELECT f.name,f.media_type,f.data FROM chat_attachments f
		LEFT JOIN attachment_requests r ON r.id=f.task_id
		LEFT JOIN work_inputs w ON w.task_id=f.task_id AND w.job_id=? AND w.answer_key IS NOT NULL
		WHERE r.id IS NOT NULL OR w.task_id IS NOT NULL ORDER BY w.task_id IS NOT NULL,r.rowid,
		(SELECT m.id FROM messages m WHERE m.task_id=f.task_id AND m.role='user'),f.position`, source, job)
}

func backgroundBrief(ctx context.Context, db queryer, job, brief string) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT q.text,a.text FROM work_inputs w
		JOIN messages q ON q.task_id=w.task_id AND q.role='assistant'
		JOIN messages a ON a.task_id=w.task_id AND a.role='user'
		WHERE w.job_id=? ORDER BY a.id`, job)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type clarification struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	var history []clarification
	for rows.Next() {
		var input clarification
		if err := rows.Scan(&input.Question, &input.Answer); err != nil {
			return "", err
		}
		history = append(history, input)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", err
	}
	if len(history) > 0 {
		data, err := json.Marshal(history)
		if err != nil {
			return "", err
		}
		brief += "\n\nResume this same assignment using the caller’s answers below. Check the workspace before repeating any work.\n" + string(data)
	}
	evidence, err := assignmentEvidence(ctx, db, job)
	return brief + evidence, err
}
