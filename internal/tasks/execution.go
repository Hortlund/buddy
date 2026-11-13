package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const executionSchema = `
CREATE TABLE IF NOT EXISTS execution_hosts (
 id TEXT PRIMARY KEY, profile TEXT NOT NULL, updated INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
 id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE,
 description TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS project_bindings (
 project_id TEXT NOT NULL REFERENCES projects(id), host_id TEXT NOT NULL,
 path TEXT NOT NULL, PRIMARY KEY(project_id,host_id)
);
CREATE TABLE IF NOT EXISTS work_execution (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id), project_id TEXT NOT NULL,
 role TEXT NOT NULL, requirements TEXT NOT NULL, environment TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS device_project_checkouts (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id), path TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS worker_sessions (
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), host_id TEXT NOT NULL,
 workspace TEXT NOT NULL, branch TEXT NOT NULL, started INTEGER NOT NULL, ended INTEGER,
 status TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS worker_sessions_task ON worker_sessions(task_id,started);
CREATE TABLE IF NOT EXISTS work_artifacts (
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), name TEXT NOT NULL,
 media_type TEXT NOT NULL, data BLOB NOT NULL, created INTEGER NOT NULL,
 UNIQUE(task_id,name)
);
`

// Profiles describe observations, never grant permissions or contain credentials.
type Capability struct {
	ID        string `json:"id"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Ready     bool   `json:"ready"`
	Detail    string `json:"detail,omitempty"`
}

type ProjectBinding struct {
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
}

type HostProfile struct {
	Slots        int              `json:"slots"`
	OS           string           `json:"os"`
	Architecture string           `json:"architecture"`
	CPUs         int              `json:"cpus"`
	Environment  string           `json:"environment"`
	Capabilities []Capability     `json:"capabilities"`
	Projects     []ProjectBinding `json:"projects,omitempty"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

func (p HostProfile) Supports(requirements []string) bool {
	return !slices.ContainsFunc(requirements, func(required string) bool {
		return !slices.ContainsFunc(p.Capabilities, func(c Capability) bool { return c.ID == required && c.Installed && c.Enabled && c.Ready })
	})
}

type Project struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Bindings    []ProjectBinding `json:"bindings,omitempty"`
}

type ExecutionPlan struct {
	ProjectID    string   `json:"project_id,omitempty"`
	Role         string   `json:"worker_role,omitempty"`
	Requirements []string `json:"requirements,omitempty"`
	Environment  string   `json:"environment,omitempty"`
}

type WorkerSession struct {
	ID        string     `json:"id"`
	Host      string     `json:"host"`
	Workspace string     `json:"workspace"`
	Branch    string     `json:"branch,omitempty"`
	Started   time.Time  `json:"started"`
	Ended     *time.Time `json:"ended,omitempty"`
	Status    Status     `json:"status"`
}

func (s *Store) SaveHostProfile(ctx context.Context, host string, profile HostProfile) error {
	if len(profile.Capabilities) > 64 || len(profile.Projects) > 64 || !validTitle(profile.Environment, 80) ||
		!validTitle(profile.OS, 32) || !validTitle(profile.Architecture, 32) || profile.CPUs < 1 || profile.CPUs > 4096 {
		return &InputError{"Invalid host capability report."}
	}
	seen := map[string]bool{}
	for _, capability := range profile.Capabilities {
		if !mcpName.MatchString(capability.ID) || len(capability.Detail) > 300 || seen[capability.ID] {
			return &InputError{"Invalid host capability."}
		}
		seen[capability.ID] = true
	}
	seenProjects := map[string]bool{}
	for _, binding := range profile.Projects {
		if !mcpName.MatchString(binding.ProjectID) || binding.Path == "" || len(binding.Path) > 4096 || strings.ContainsAny(binding.Path, "\x00\r\n") {
			return &InputError{"Invalid project checkout."}
		}
		if seenProjects[binding.ProjectID] {
			return &InputError{"A host can report one checkout per project."}
		}
		seenProjects[binding.ProjectID] = true
	}
	if profile.Slots == 0 {
		profile.Slots = 1
	}
	if profile.Slots < 1 || profile.Slots > 32 {
		return &InputError{"Invalid host capacity."}
	}
	profile.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if host != "server" {
		if err := touchDevice(ctx, tx, host, time.Now()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO execution_hosts VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,updated=excluded.updated`, host, string(data), profile.UpdatedAt.UnixMicro()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM project_bindings WHERE host_id=?", host); err != nil {
		return err
	}
	for _, binding := range profile.Projects {
		if _, err = tx.ExecContext(ctx, `INSERT INTO project_bindings SELECT id,?,? FROM projects WHERE id=? AND archived=0
			ON CONFLICT(project_id,host_id) DO UPDATE SET path=excluded.path`, host, binding.Path, binding.ProjectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func readHostProfile(ctx context.Context, db queryer, host string) (HostProfile, error) {
	var data string
	var profile HostProfile
	err := db.QueryRowContext(ctx, "SELECT profile FROM execution_hosts WHERE id=?", host).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, nil
	}
	if err != nil {
		return profile, err
	}
	err = json.Unmarshal([]byte(data), &profile)
	return profile, err
}

func (s *Store) HostProfile(ctx context.Context, host string) (HostProfile, error) {
	return readHostProfile(ctx, s.db, host)
}

func (s *Store) Projects(ctx context.Context) ([]Project, error) { return readProjects(ctx, s.db) }
func readProjects(ctx context.Context, db queryer) ([]Project, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,name,description FROM projects WHERE archived=0 ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description); err != nil {
			rows.Close()
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, errors.Join(rows.Err(), rows.Close())
}

func (s *Store) SaveProject(ctx context.Context, p Project) (Project, error) {
	p.Name, p.Description = strings.TrimSpace(p.Name), strings.TrimSpace(p.Description)
	if !validTitle(p.Name, 100) || len(p.Description) > 2000 {
		return p, &InputError{"Give the project a name and a short description."}
	}
	if p.ID == "" {
		p.ID = "prj_" + rand.Text()
	}
	if !mcpName.MatchString(p.ID) {
		return p, &InputError{"Invalid project ID."}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(id,name,description) VALUES (?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description WHERE archived=0`, p.ID, p.Name, p.Description)
	return p, err
}

func (s *Store) ArchiveProject(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_execution e JOIN tasks t ON t.id=e.task_id
		WHERE e.project_id=? AND t.status IN ('queued','running','needs_input','cancelling'))`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return &InputError{"Finish or stop this project's active work before removing it."}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE projects SET archived=1 WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func validateExecution(plan *ExecutionPlan) error {
	if plan.Role == "" {
		plan.Role = "general"
	}
	if !mcpName.MatchString(plan.Role) || (plan.ProjectID != "" && !mcpName.MatchString(plan.ProjectID)) || len(plan.Requirements) > 16 || len(plan.Environment) > 80 {
		return &InputError{"Invalid worker requirements."}
	}
	for i, id := range plan.Requirements {
		if !mcpName.MatchString(id) || slices.Contains(plan.Requirements[:i], id) {
			return &InputError{"Use distinct capability IDs for worker requirements."}
		}
	}
	return nil
}

func executionWorkspace(ctx context.Context, db queryer, host string, plan ExecutionPlan) (string, string, error) {
	profile, err := readHostProfile(ctx, db, host)
	if err != nil {
		return "", "", err
	}
	for _, capability := range profile.Capabilities {
		if capability.ID == "codex" && (!capability.Installed || !capability.Enabled || !capability.Ready) {
			return "", "The model runtime on this host needs installation or sign-in.", nil
		}
	}
	if !profile.Supports(plan.Requirements) {
		return "", "Required tools are unavailable or disabled on this host.", nil
	}
	if plan.Environment != "" && plan.Environment != profile.Environment {
		return "", "Waiting for the requested execution environment.", nil
	}
	if plan.ProjectID == "" {
		return "", "", nil
	}
	return projectCheckout(ctx, db, host, plan.ProjectID)
}

func projectCheckout(ctx context.Context, db queryer, host, project string) (string, string, error) {
	var workspace string
	err := db.QueryRowContext(ctx, `SELECT b.path FROM project_bindings b JOIN projects p ON p.id=b.project_id
		WHERE b.project_id=? AND b.host_id=? AND p.archived=0`, project, host).Scan(&workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "This project has no checkout configured on this host.", nil
	}
	return workspace, "", err
}

const changedBudCheckout = "The project checkout changed or was unavailable when this work was drafted. Use the original checkout or request the work again."

func validateSavedCheckout(ctx context.Context, tx *sql.Tx, draft, project, host, path string, namedHost bool) (string, error) {
	if draft == "" || project == "" {
		return "", nil
	}
	saved, err := savedProjectCheckout(ctx, tx, draft, project, host, namedHost)
	if err != nil {
		return "", err
	}
	if saved == "" || path != saved {
		return changedBudCheckout, nil
	}
	return "", nil
}

func readExecutionPlan(ctx context.Context, db queryer, job string) (ExecutionPlan, error) {
	var plan ExecutionPlan
	var requirements string
	err := db.QueryRowContext(ctx, "SELECT project_id,role,requirements,environment FROM work_execution WHERE task_id=?", job).
		Scan(&plan.ProjectID, &plan.Role, &requirements, &plan.Environment)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	err = json.Unmarshal([]byte(requirements), &plan.Requirements)
	return plan, err
}

func saveExecutionPlan(ctx context.Context, tx *sql.Tx, job string, plan ExecutionPlan) error {
	if err := validateExecution(&plan); err != nil {
		return err
	}
	data, _ := json.Marshal(plan.Requirements)
	_, err := tx.ExecContext(ctx, "INSERT INTO work_execution VALUES (?,?,?,?,?)", job, plan.ProjectID, plan.Role, string(data), plan.Environment)
	return err
}

func (s *Store) StartWorkerSession(ctx context.Context, job, host, workspace, branch string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status Status
	if err = tx.QueryRowContext(ctx, "SELECT status FROM tasks WHERE id=?", job).Scan(&status); err != nil {
		return err
	}
	if status != Running {
		return ErrInactive
	}
	if _, err = tx.ExecContext(ctx, `UPDATE worker_sessions SET ended=?,status='interrupted' WHERE task_id=? AND ended IS NULL`, time.Now().UnixMicro(), job); err != nil {
		return err
	}
	if host == "server" {
		if _, err = tx.ExecContext(ctx, `UPDATE file_reference_workspaces SET workspace=? WHERE task_id=?
            AND EXISTS(SELECT 1 FROM work_execution e WHERE e.task_id=? AND e.project_id<>'')`, workspace, job, job); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worker_sessions VALUES (?,?,?,?,?,?,NULL,'running')`, "run_"+rand.Text(), job, host, workspace, branch, time.Now().UnixMicro())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func workerSessions(ctx context.Context, db queryer, job string) ([]WorkerSession, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.id,r.host_id,r.workspace,r.branch,r.started,COALESCE(r.ended,t.finished),
		CASE WHEN r.ended IS NULL THEN t.status ELSE r.status END FROM worker_sessions r JOIN tasks t ON t.id=r.task_id
		WHERE r.task_id=? ORDER BY r.started DESC LIMIT 30`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []WorkerSession
	for rows.Next() {
		var session WorkerSession
		var start int64
		var end sql.NullInt64
		if err := rows.Scan(&session.ID, &session.Host, &session.Workspace, &session.Branch, &start, &end, &session.Status); err != nil {
			return nil, err
		}
		session.Started = time.UnixMicro(start).UTC()
		if end.Valid {
			at := time.UnixMicro(end.Int64).UTC()
			session.Ended = &at
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func cleanBindingPath(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", &InputError{"Use an absolute project checkout path."}
	}
	return checkWorkspace(root)
}

func routeExecution(ctx context.Context, tx *sql.Tx, agent *Agent, request *workRequest) error {
	if err := validateExecution(&request.plan); err != nil {
		return err
	}
	plan := request.plan
	explicitServer := request.device == "server"
	if explicitServer {
		request.device = ""
	}
	if plan.ProjectID == "" && len(plan.Requirements) == 0 && plan.Environment == "" {
		return nil
	}
	if plan.ProjectID != "" {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND archived=0)", plan.ProjectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return &InputError{"Choose an existing project from the project directory."}
		}
	}
	if request.device != "" && request.device != DevicePool {
		if request.projectDraft != "" && plan.ProjectID != "" {
			path, _, err := projectCheckout(ctx, tx, request.device, plan.ProjectID)
			if err != nil {
				return err
			}
			reason, err := validateSavedCheckout(ctx, tx, request.projectDraft, plan.ProjectID, request.device, path, true)
			if err != nil {
				return err
			}
			if reason != "" {
				return &InputError{reason}
			}
		}
		return nil
	} // Explicit offline hosts keep their queue.
	if request.device == "" {
		workspace, reason, err := executionWorkspace(ctx, tx, "server", plan)
		if err != nil {
			return err
		}
		if reason == "" {
			reason, err = validateSavedCheckout(ctx, tx, request.projectDraft, plan.ProjectID, "server", workspace, explicitServer)
			if err != nil {
				return err
			}
		}
		if reason != "" && explicitServer {
			return &InputError{reason}
		}
		if reason == "" {
			if workspace != "" {
				agent.Workspace = workspace
				agent.WorkspaceNeedsReview = false
			}
			return nil
		}
	}
	hosts, err := eligiblePoolCheckouts(ctx, tx, plan, agent.AllowEdits, false)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		reason, err := validateSavedCheckout(ctx, tx, request.projectDraft, plan.ProjectID, host.id, host.path, false)
		if err != nil {
			return err
		}
		if reason == "" {
			if request.device == "" {
				request.device = host.id
			}
			return nil
		}
	}
	if request.projectDraft != "" && plan.ProjectID != "" {
		return &InputError{"No ready host has the saved project checkout. Choose Create Bud only or request the work again."}
	}
	return &InputError{"No ready host has the required tools and project checkout. Check host capabilities in Connections or choose a configured device."}
}

type workerCheckout struct{ id, path string }

// Offline members can still claim a queued job when they reconnect.
func eligiblePoolCheckouts(ctx context.Context, tx *sql.Tx, plan ExecutionPlan, allowEdits, includeOffline bool) ([]workerCheckout, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.id FROM devices d JOIN device_pool_members p ON p.device_id=d.id
		WHERE d.revoked=0 AND (? OR d.last_seen>?) AND (?=0 OR p.allow_edits=1)
		ORDER BY (SELECT count(*) FROM device_runs dr JOIN tasks t ON t.id=dr.task_id WHERE dr.device_id=d.id AND t.status IN ('running','cancelling')),d.name`, includeOffline, time.Now().Add(-deviceLeaseDuration).UnixMicro(), allowEdits)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var hosts []workerCheckout
	for _, host := range ids {
		workspace, reason, err := executionWorkspace(ctx, tx, host, plan)
		if err != nil {
			return nil, err
		}
		if reason == "" {
			hosts = append(hosts, workerCheckout{host, workspace})
		}
	}
	return hosts, nil
}

func workDependencies(ctx context.Context, db queryer, job string) ([]TeamAssignment, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.id,td.agent_id,a.name,td.title,t.status,ct.conversation_id FROM work_dependencies d
		JOIN tasks t ON t.id=d.prerequisite_id JOIN turn_details td ON td.task_id=t.id JOIN agents a ON a.id=td.agent_id
		JOIN chat_turns ct ON ct.task_id=t.id WHERE d.task_id=?`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TeamAssignment
	for rows.Next() {
		var item TeamAssignment
		if err := rows.Scan(&item.ID, &item.AgentID, &item.AgentName, &item.Title, &item.Status, &item.ConversationID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Hosts(ctx context.Context) ([]Device, error) {
	devices, err := s.Devices(ctx)
	if err != nil {
		return nil, err
	}
	profile, err := s.HostProfile(ctx, "server")
	if err != nil {
		return nil, err
	}
	return append([]Device{{ID: "server", Name: "Buddy server", Online: true, Seen: true, Profile: &profile}}, devices...), nil
}

func delegatedWork(ctx context.Context, db queryer, job string) ([]TeamAssignment, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.id,td.agent_id,a.name,td.title,t.status,ct.conversation_id
		FROM turn_details td JOIN tasks t ON t.id=td.task_id JOIN agents a ON a.id=td.agent_id
		JOIN chat_turns ct ON ct.task_id=t.id WHERE td.parent_id=? AND t.kind='background_work' ORDER BY t.created`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var children []TeamAssignment
	for rows.Next() {
		var child TeamAssignment
		if err := rows.Scan(&child.ID, &child.AgentID, &child.AgentName, &child.Title, &child.Status, &child.ConversationID); err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	return children, rows.Err()
}

func (s *Store) projectFileReference(ctx context.Context, bud, job, workspace string) (bool, error) {
	var valid bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_execution e JOIN tasks t ON t.id=e.task_id
        JOIN turn_details td ON td.task_id=t.id JOIN projects p ON p.id=e.project_id
        JOIN project_bindings b ON b.project_id=e.project_id AND b.host_id='server'
        WHERE t.id=? AND td.agent_id=? AND b.path=t.repo AND p.archived=0
        AND NOT EXISTS(SELECT 1 FROM removed_buds r WHERE r.agent_id=td.agent_id)
        AND (?=t.repo OR EXISTS(SELECT 1 FROM worker_sessions r WHERE r.task_id=t.id AND r.host_id='server' AND r.workspace=?)))`, job, bud, workspace, workspace).Scan(&valid)
	return valid, err
}
