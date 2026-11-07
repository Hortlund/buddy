package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const deviceSchema = `
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	last_seen INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS device_runs (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	device_id TEXT NOT NULL REFERENCES devices(id),
	lease TEXT NOT NULL DEFAULT '',
	expires INTEGER NOT NULL DEFAULT 0,
	reported INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS device_runs_device ON device_runs(device_id,expires);
CREATE TABLE IF NOT EXISTS routine_devices (
	routine_id TEXT PRIMARY KEY REFERENCES routines(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL REFERENCES devices(id)
);
CREATE TABLE IF NOT EXISTS device_pool_members (
	device_id TEXT PRIMARY KEY REFERENCES devices(id),
	allow_edits INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pooled_work (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id)
);
`

const deviceLeaseDuration = 25 * time.Second
const deviceDeliveryDuration = 150 * time.Second

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	Seen      bool   `json:"seen,omitempty"`
	Pool      bool   `json:"pool,omitempty"`
	PoolEdits bool   `json:"pool_edits,omitempty"`
}

type DeviceCredential struct {
	Device
	Token string `json:"token"`
}

type DeviceJob struct {
	BuddyTeam       TeamContext      `json:"buddy_team"`
	Skill           *SkillInvocation `json:"skill,omitempty"`
	Language        string           `json:"language,omitempty"`
	ID              string           `json:"id"`
	Lease           string           `json:"lease"`
	Seconds         int              `json:"timeout_seconds"`
	Agent           Agent            `json:"agent"`
	Title           string           `json:"title"`
	Brief           string           `json:"brief"`
	Personality     string           `json:"personality,omitempty"`
	Attachments     []Attachment     `json:"attachments,omitempty"`
	NeedsStart      bool             `json:"needs_start,omitempty"`
	CanRequestInput bool             `json:"can_request_input,omitempty"`
}

type DevicePulse struct {
	Lease           string       `json:"lease"`
	Kind            ActivityKind `json:"kind,omitempty"`
	Update          string       `json:"update,omitempty"`
	CompletedUpdate bool         `json:"completed_update,omitempty"`
}

type DeviceResult struct {
	Lease  string `json:"lease"`
	Status Status `json:"status"`
	Reply  string `json:"reply"`
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (s *Store) EnrollDevice(ctx context.Context, name string) (DeviceCredential, error) {
	name = strings.TrimSpace(name)
	if !validTitle(name, 80) || strings.EqualFold(name, "server") {
		return DeviceCredential{}, &InputError{"Choose a device name other than Server."}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return DeviceCredential{}, err
	}
	credential := DeviceCredential{Device: Device{ID: "dev_" + rand.Text(), Name: name}, Token: hex.EncodeToString(raw[:])}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceCredential{}, err
	}
	defer tx.Rollback()
	var existing bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM devices WHERE revoked=0 AND name=? COLLATE NOCASE)", name).Scan(&existing); err != nil {
		return DeviceCredential{}, err
	}
	if existing {
		return DeviceCredential{}, &InputError{"That device name is already in use."}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO devices(id,name,token_hash) VALUES (?,?,?)", credential.ID, name, tokenHash(credential.Token)); err != nil {
		return DeviceCredential{}, err
	}
	return credential, tx.Commit()
}

func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.name,d.last_seen>?,d.last_seen<>0,p.device_id IS NOT NULL,COALESCE(p.allow_edits,0)
		FROM devices d LEFT JOIN device_pool_members p ON p.device_id=d.id WHERE d.revoked=0 ORDER BY d.name,d.id`,
		time.Now().Add(-deviceLeaseDuration).UnixMicro())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		var device Device
		if err := rows.Scan(&device.ID, &device.Name, &device.Online, &device.Seen, &device.Pool, &device.PoolEdits); err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *Store) AuthenticateDevice(ctx context.Context, id, token string) bool {
	if len(token) != 64 {
		return false
	}
	var matched bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM devices WHERE id=? AND token_hash=? AND revoked=0)",
		id, tokenHash(token)).Scan(&matched)
	return err == nil && matched
}

func deviceName(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, "SELECT name FROM devices WHERE id=? AND revoked=0", id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		err = &InputError{"Choose a connected device or use the Buddy server."}
	}
	return name, err
}

func touchDevice(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	result, err := tx.ExecContext(ctx, "UPDATE devices SET last_seen=? WHERE id=? AND revoked=0", now.UnixMicro(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

func (s *Store) ClaimDeviceJob(ctx context.Context, id string) (*DeviceJob, error) {
	return s.claimDeviceJob(ctx, id, false, false, false, false)
}

func (s *Store) ReserveDeviceJob(ctx context.Context, id string) (*DeviceJob, error) {
	return s.ReserveDeviceWork(ctx, id, false, false)
}

func (s *Store) ReserveDeviceWork(ctx context.Context, id string, pool, edits bool) (*DeviceJob, error) {
	return s.claimDeviceJob(ctx, id, true, pool, edits, false)
}

func (s *Store) ReserveDeviceSkillWork(ctx context.Context, id string, pool, edits bool) (*DeviceJob, error) {
	return s.claimDeviceJob(ctx, id, true, pool, edits, true)
}

func (s *Store) claimDeviceJob(ctx context.Context, id string, reserveDelivery, pool, edits, supportsSkills bool) (*DeviceJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now()
	if err := touchDevice(ctx, tx, id, now); err != nil {
		return nil, err
	}
	if pool {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_pool_members VALUES (?,?)
			ON CONFLICT(device_id) DO UPDATE SET allow_edits=excluded.allow_edits
			WHERE allow_edits<>excluded.allow_edits`, id, edits); err != nil {
			return nil, err
		}
	} else if _, err := tx.ExecContext(ctx, "DELETE FROM device_pool_members WHERE device_id=?", id); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT t.id,COALESCE(td.parent_id,''),dr.task_id IS NULL FROM tasks t
		JOIN turn_details td ON td.task_id=t.id JOIN chat_turns ct ON ct.task_id=t.id
		LEFT JOIN device_runs dr ON dr.task_id=t.id LEFT JOIN pooled_work p ON p.task_id=t.id
		WHERE t.status='queued' AND (dr.device_id=? OR (? AND dr.task_id IS NULL AND p.task_id IS NOT NULL
			AND (? OR ct.allow_edits=0))) AND NOT EXISTS(
			SELECT 1 FROM device_runs busy JOIN tasks active ON active.id=busy.task_id
			WHERE busy.device_id=? AND (active.status IN ('running','cancelling')
				OR (active.status='queued' AND busy.lease<>'' AND busy.expires>?)))
		AND `+dependenciesReady+`
		ORDER BY dr.task_id IS NULL,t.rowid`, id, pool, edits, id, now.UnixMicro())
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id, parent string
		unassigned bool
	}
	var queued []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.parent, &item.unassigned); err != nil {
			rows.Close()
			return nil, err
		}
		queued = append(queued, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, item := range queued {
		skill, err := skillForTurn(ctx, tx, item.id, item.parent)
		if err != nil {
			return nil, err
		}
		if skill != nil && !supportsSkills {
			const message = "Update buddyworker on this device to run jobs with skills."
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET summary=? WHERE id=? AND summary<>?", message, item.id, message); err != nil {
				return nil, err
			}
			continue
		}
		source, err := handoffAttachmentSource(ctx, tx, item.id, item.parent)
		if err != nil {
			return nil, err
		}
		files, err := readWorkAttachments(ctx, tx, item.id, source)
		if err != nil {
			return nil, err
		}
		if len(files) != 0 && !reserveDelivery {
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET summary=? WHERE id=? AND summary<>?",
				DeviceAttachmentUpgrade, item.id, DeviceAttachmentUpgrade); err != nil {
				return nil, err
			}
			continue
		}
		job := &DeviceJob{ID: item.id, Lease: rand.Text(), NeedsStart: reserveDelivery && (len(files) != 0 || skill != nil),
			Attachments: files, CanRequestInput: reserveDelivery, Skill: skill}
		var conversation string
		if err := tx.QueryRowContext(ctx, `
			SELECT t.timeout,td.agent_id,ct.agent_name,ct.instructions,ct.allow_edits,tm.model,td.agent_role,td.title,td.brief,(SELECT chat FROM language_preferences WHERE id=1),ct.conversation_id
			FROM tasks t JOIN chat_turns ct ON ct.task_id=t.id
			JOIN turn_details td ON td.task_id=t.id JOIN turn_models tm ON tm.task_id=t.id
			WHERE t.id=?`, item.id).Scan(&job.Seconds, &job.Agent.ID, &job.Agent.Name, &job.Agent.Instructions,
			&job.Agent.AllowEdits, &job.Agent.Model, &job.Agent.Role, &job.Title, &job.Brief, &job.Language, &conversation); err != nil {
			return nil, err
		}
		job.BuddyTeam, err = readTeamContext(ctx, tx, conversation, job.Agent.ID)
		if err != nil {
			return nil, err
		}
		job.BuddyTeam.CurrentTaskID = job.ID
		job.Personality, err = s.personality(job.Agent.ID)
		if err != nil {
			const message = "Buddy couldn't read the agent's AGENTS.md."
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status='failed',summary=?,finished=? WHERE id=?",
				message, now.UnixMicro(), item.id); err != nil {
				return nil, err
			}
			if err := addReply(ctx, tx, item.id, message); err != nil {
				return nil, err
			}
			return nil, tx.Commit()
		}
		job.Brief, err = backgroundBrief(ctx, tx, item.id, job.Brief)
		if err != nil {
			return nil, err
		}
		if item.unassigned {
			if _, err := tx.ExecContext(ctx, "INSERT INTO device_runs(task_id,device_id) VALUES (?,?)", item.id, id); err != nil {
				return nil, err
			}
		}
		duration := min(deviceLeaseDuration, time.Duration(job.Seconds)*time.Second)
		if job.NeedsStart {
			duration = deviceDeliveryDuration
			message := "Delivering attachments to the device."
			if len(files) == 0 {
				message = "Delivering the skill to the device."
			}
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET summary=? WHERE id=?", message, item.id)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE tasks SET status='running',started=?,summary=? WHERE id=?",
				now.UnixMicro(), activityLabels[Starting], item.id)
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE device_runs SET lease=?,expires=? WHERE task_id=?",
			job.Lease, now.Add(duration).UnixMicro(), item.id); err != nil {
			return nil, err
		}
		return job, tx.Commit()
	}
	return nil, tx.Commit()
}

// StartDeviceJob acknowledges that the full payload reached the device before
// starting its execution deadline. A lost acknowledgment can be retried.
func (s *Store) StartDeviceJob(ctx context.Context, id, task, lease string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	if err := touchDevice(ctx, tx, id, now); err != nil {
		return err
	}
	var status Status
	var seconds int
	err = tx.QueryRowContext(ctx, `SELECT t.status,t.timeout FROM device_runs dr JOIN tasks t ON t.id=dr.task_id
		WHERE dr.device_id=? AND dr.task_id=? AND dr.lease=? AND dr.lease<>'' AND dr.expires>?`,
		id, task, lease, now.UnixMicro()).Scan(&status, &seconds)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInactive
	}
	if err != nil {
		return err
	}
	switch status {
	case Running:
		return tx.Commit()
	case Queued:
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status='running',started=?,summary=? WHERE id=?",
			now.UnixMicro(), activityLabels[Starting], task); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE device_runs SET expires=? WHERE task_id=?",
			now.Add(min(deviceLeaseDuration, time.Duration(seconds)*time.Second)).UnixMicro(), task); err != nil {
			return err
		}
		return tx.Commit()
	default:
		return ErrInactive
	}
}

func (s *Store) PulseDeviceJob(ctx context.Context, id, task string, pulse DevicePulse) (Status, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now()
	if err := touchDevice(ctx, tx, id, now); err != nil {
		return "", err
	}
	var status Status
	var deadline int64
	err = tx.QueryRowContext(ctx, `SELECT t.status,t.started+t.timeout*1000000
		FROM device_runs dr JOIN tasks t ON t.id=dr.task_id
		WHERE dr.task_id=? AND dr.device_id=? AND dr.lease=? AND dr.lease<>'' AND dr.expires>?
			AND t.started IS NOT NULL`,
		task, id, pulse.Lease, now.UnixMicro()).Scan(&status, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrInactive
	}
	if err != nil {
		return "", err
	}
	if deadline <= now.UnixMicro() {
		return "", ErrInactive
	}
	if status == Running {
		if _, err := tx.ExecContext(ctx, "UPDATE device_runs SET expires=? WHERE task_id=?",
			min(deadline, now.Add(deviceLeaseDuration).UnixMicro()), task); err != nil {
			return "", err
		}
		if label, valid := activityLabels[pulse.Kind]; valid {
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET summary=? WHERE id=?", label, task); err != nil {
				return "", err
			}
		}
		if note := strings.TrimSpace(pulse.Update); note != "" {
			stream := "commentary"
			if pulse.CompletedUpdate {
				stream = "work_update"
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO events(task_id,at,stream,text) SELECT ?,?,?,?
				WHERE ?<>COALESCE((SELECT text FROM events WHERE task_id=? AND stream IN ('commentary','work_update') ORDER BY seq DESC LIMIT 1),'')`,
				task, now.UnixMicro(), stream, note, note, task); err != nil {
				return "", err
			}
		}
	}
	return status, tx.Commit()
}

func (s *Store) FinishDeviceJob(ctx context.Context, id, task string, report DeviceResult) error {
	switch report.Status {
	case Succeeded, Failed, Interrupted:
	case NeedsInput:
		if strings.TrimSpace(report.Reply) == "" {
			return &InputError{"A job waiting for input needs a question."}
		}
	default:
		return &InputError{"Invalid device result."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	if err := touchDevice(ctx, tx, id, now); err != nil {
		return err
	}
	var expires int64
	var reported bool
	err = tx.QueryRowContext(ctx, `SELECT min(dr.expires,t.started+t.timeout*1000000),dr.reported
		FROM device_runs dr JOIN tasks t ON t.id=dr.task_id
		WHERE dr.task_id=? AND dr.device_id=? AND dr.lease=? AND dr.lease<>'' AND t.started IS NOT NULL`,
		task, id, report.Lease).Scan(&expires, &reported)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInactive
	}
	if err != nil {
		return err
	}
	if reported {
		return tx.Commit()
	}
	if expires <= now.UnixMicro() {
		return ErrInactive
	}
	summary := "Finished."
	if report.Status == NeedsInput {
		summary = "Waiting for your reply."
	} else if report.Status != Succeeded {
		summary = "Couldn't finish on the device."
	}
	if err := finishTask(ctx, tx, task, report.Status, summary, clipReply(report.Reply), nil); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE device_runs SET reported=1 WHERE task_id=?", task); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ExpireDeviceJobs(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := interruptJobs(ctx, tx, `SELECT t.id FROM tasks t JOIN device_runs dr ON dr.task_id=t.id
		WHERE t.status IN ('running','cancelling') AND min(dr.expires,t.started+t.timeout*1000000)<=?`,
		"The device did not report a result before its deadline. Check it before retrying this work.", time.Now().UnixMicro()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE devices SET revoked=1 WHERE id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE routines SET enabled=0 WHERE id IN
		(SELECT routine_id FROM routine_devices WHERE device_id=?)`, id); err != nil {
		return err
	}
	if err := interruptJobs(ctx, tx, `SELECT t.id FROM tasks t JOIN device_runs dr ON dr.task_id=t.id
		WHERE t.status IN ('queued','running','needs_input','cancelling') AND dr.device_id=?`,
		"The assigned device was disconnected.", id); err != nil {
		return err
	}
	return tx.Commit()
}

func interruptJobs(ctx context.Context, tx *sql.Tx, query, message string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := closeWorkInput(ctx, tx, id, Interrupted); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET status='interrupted',summary=?,finished=? WHERE id=?",
			message, time.Now().UnixMicro(), id); err != nil {
			return err
		}
		if err := addReply(ctx, tx, id, message); err != nil {
			return err
		}
	}
	return nil
}
