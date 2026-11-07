package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"time"
)

const automationStateSchema = `
CREATE TABLE IF NOT EXISTS automation_control(task_id TEXT PRIMARY KEY REFERENCES tasks(id),paused INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS execution_frames(task_id TEXT PRIMARY KEY REFERENCES tasks(id),frame BLOB NOT NULL);
`

func (s *Store) SetAutomationPaused(ctx context.Context, job string, paused bool) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO automation_control SELECT t.id,? FROM tasks t JOIN work_execution e ON e.task_id=t.id
		WHERE t.id=? AND t.status='running' AND EXISTS(SELECT 1 FROM json_each(e.requirements) WHERE value IN ('desktop','browser'))
		ON CONFLICT(task_id) DO UPDATE SET paused=excluded.paused`, paused, job)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrInactive
	}
	return err
}

func (s *Store) SaveExecutionFrame(ctx context.Context, job, device, lease string, frame ExecutionFrame) error {
	if !validExecutionImage(frame.Data) {
		return &InputError{"Invalid execution screenshot."}
	}
	if frame.Provider != "cua" && frame.Provider != "playwright" {
		return &InputError{"Invalid execution provider."}
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO execution_frames SELECT t.id,? FROM tasks t JOIN device_runs dr ON dr.task_id=t.id
		WHERE t.id=? AND dr.device_id=? AND dr.lease=? AND dr.expires>? AND t.status='running'
		ON CONFLICT(task_id) DO UPDATE SET frame=excluded.frame`, data, job, device, lease, time.Now().UnixMicro())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrInactive
	}
	return err
}

func (s *Store) StoredExecutionFrame(ctx context.Context, job string) (ExecutionFrame, error) {
	var data []byte
	var frame ExecutionFrame
	err := s.db.QueryRowContext(ctx, "SELECT frame FROM execution_frames WHERE task_id=?", job).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return frame, nil
	}
	if err != nil {
		return frame, err
	}
	err = json.Unmarshal(data, &frame)
	return frame, err
}

type LumeVM struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	IP     string `json:"ipAddress,omitempty"`
}

func LumeVMs(ctx context.Context) ([]LumeVM, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "lume", "ls", "--format", "json").Output()
	if err != nil {
		return nil, &InputError{"Install Lume on an Apple Silicon Mac to manage its VMs."}
	}
	if len(data) > 1<<20 {
		return nil, &InputError{"Lume returned an oversized inventory."}
	}
	var vms []LumeVM
	err = json.Unmarshal(data, &vms)
	return vms, err
}

func ChangeLumeVM(ctx context.Context, name, action string) error {
	if !mcpName.MatchString(name) {
		return &InputError{"Choose an existing Lume VM."}
	}
	vms, err := LumeVMs(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(vms, func(vm LumeVM) bool { return vm.Name == name }) {
		return ErrNotFound
	}
	args := []string{"run", name, "--detach", "--display", "none"}
	if action == "stop" {
		args = []string{"shutdown", name}
	} else if action != "start" {
		return &InputError{"Choose start or stop."}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "lume", args...).Run() != nil {
		return &InputError{"Lume couldn't change this VM. Check its logs and guest setup on the host."}
	}
	return nil
}
