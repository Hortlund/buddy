package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const turnDetailsTable = `(
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	agent_id TEXT NOT NULL REFERENCES agents(id),
	agent_role TEXT NOT NULL,
	origin TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	brief TEXT NOT NULL DEFAULT '',
	parent_id TEXT REFERENCES tasks(id),
	routine_id TEXT
)`

const workspaceSchema = `
CREATE TABLE IF NOT EXISTS managed_workspace_root (
	id INTEGER PRIMARY KEY CHECK(id=1),
	path TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_roles (
	agent_id TEXT PRIMARY KEY REFERENCES agents(id),
	role TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS workspace_primary (
	id INTEGER PRIMARY KEY CHECK(id=1),
	agent_id TEXT NOT NULL REFERENCES agents(id)
);
INSERT OR IGNORE INTO workspace_primary VALUES (1,'` + DefaultAgentID + `');
CREATE TABLE IF NOT EXISTS group_chats (
	conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
	name TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS group_members (
	conversation_id TEXT NOT NULL REFERENCES group_chats(conversation_id),
	agent_id TEXT NOT NULL REFERENCES agents(id),
	position INTEGER NOT NULL,
	PRIMARY KEY(conversation_id,agent_id)
);
CREATE TABLE IF NOT EXISTS turn_details ` + turnDetailsTable + `;
CREATE INDEX IF NOT EXISTS turn_details_routine ON turn_details(routine_id);
CREATE TABLE IF NOT EXISTS chat_default_recipients (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS background_reads (
	conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),
	through_id INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS chat_unread_baseline (
	id INTEGER PRIMARY KEY CHECK(id=1),
	through_id INTEGER NOT NULL
);
INSERT OR IGNORE INTO chat_unread_baseline SELECT 1,COALESCE(max(id),0) FROM messages;
CREATE TABLE IF NOT EXISTS routines (
	id TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL REFERENCES conversations(id),
	agent_id TEXT NOT NULL REFERENCES agents(id),
	name TEXT NOT NULL,
	prompt TEXT NOT NULL,
	minutes INTEGER NOT NULL,
	next_at INTEGER NOT NULL,
	enabled INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS routines_due ON routines(enabled,next_at);
CREATE TABLE IF NOT EXISTS restored_workspace_agents (
	agent_id TEXT PRIMARY KEY REFERENCES agents(id),
	path TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS restored_workspace_tasks (
	task_id TEXT PRIMARY KEY REFERENCES tasks(id),
	agent_id TEXT NOT NULL REFERENCES restored_workspace_agents(agent_id) ON DELETE CASCADE
);
`

type AgentRef struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Role            string  `json:"role"`
	ConversationID  string  `json:"conversation_id,omitempty"`
	HasNewResult    bool    `json:"has_new_result,omitempty"`
	UnreadThrough   int64   `json:"unread_through,omitempty"`
	Avatar          *Avatar `json:"avatar,omitempty"`
	PictureRevision string  `json:"picture_revision,omitempty"`
}

type ChatRef struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	HasNewResult  bool   `json:"has_new_result,omitempty"`
	UnreadThrough int64  `json:"unread_through,omitempty"`
	LeadAgentID   string `json:"lead_agent_id,omitempty"`
}

type Directory struct {
	Agents         []AgentRef  `json:"agents"`
	RemovedAgents  []AgentRef  `json:"removed_agents,omitempty"`
	Groups         []ChatRef   `json:"groups"`
	ArchivedGroups []ChatRef   `json:"archived_groups,omitempty"`
	PrimaryAgentID string      `json:"primary_agent_id"`
	AutonomyUnread int         `json:"autonomy_unread,omitempty"`
	Skills         []SkillInfo `json:"skills,omitempty"`
}

type GroupRequest struct {
	Name        string   `json:"name"`
	Members     []string `json:"members"`
	LeadAgentID string   `json:"lead_agent_id,omitempty"`
}

type workspaceEntry struct {
	Name     string    `json:"name"`
	Folder   bool      `json:"folder"`
	Modified time.Time `json:"modified"`
}

type Workspace struct {
	Path    string           `json:"path"`
	Entries []workspaceEntry `json:"entries"`
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type writeQueryer interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// EnableWorkspaces runs once, before the server starts accepting requests.
func (s *Store) EnableWorkspaces(ctx context.Context, root string) error {
	var previous string
	err := s.db.QueryRowContext(ctx, "SELECT path FROM managed_workspace_root WHERE id=1").Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous != "" && previous != root {
		if err := s.useMovedWorkspaceRoot(ctx, previous, root); err != nil {
			return err
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM agents WHERE workspace=''")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.workspaceRoot = root
	for _, id := range ids {
		path, err := s.managedWorkspace(id)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE agents SET workspace=?,allow_edits=1 WHERE id=? AND workspace=''", path, id); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, "INSERT OR IGNORE INTO managed_workspace_root VALUES (1,?)", root)
	return err
}

func (s *Store) useMovedWorkspaceRoot(ctx context.Context, previous, root string) error {
	// Check the copied managed folders before changing any paths.
	prefix := previous + string(filepath.Separator)
	rows, err := s.db.QueryContext(ctx, `SELECT workspace FROM agents WHERE workspace=? OR substr(workspace,1,length(?))=?
		UNION SELECT repo FROM tasks WHERE status IN ('queued','needs_input') AND kind IN (?,?,?) AND (repo=? OR substr(repo,1,length(?))=?)`,
		previous, prefix, prefix, AgentTurn, BackgroundWork, RepoCheck, previous, prefix, prefix)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		newPath := filepath.Join(root, strings.TrimPrefix(strings.TrimPrefix(path, previous), string(filepath.Separator)))
		resolved, err := filepath.EvalSymlinks(newPath)
		if err != nil || resolved != newPath {
			return errors.New("Copy the managed workspace folders to the new root before moving the server.")
		}
		if info, err := os.Stat(newPath); err != nil || !info.IsDir() {
			return errors.New("A managed workspace folder is missing on this server.")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return s.RelocateManagedWorkspaces(ctx, previous, root)
}

func (s *Store) managedWorkspace(id string) (string, error) {
	if s.workspaceRoot == "" || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return "", errors.New("invalid Buddy managed workspace")
	}
	path := filepath.Join(s.workspaceRoot, id)
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("Buddy managed workspace must be a directory")
	}
	return path, nil
}

func (s *Store) Workspace(ctx context.Context, id string) (Workspace, error) {
	var out Workspace
	agent, err := s.Agent(ctx, id)
	if err != nil {
		return out, err
	}
	if agent.WorkspaceNeedsReview {
		return out, &InputError{WorkspaceReviewMessage}
	}
	out.Path = agent.Workspace
	if out.Path == "" {
		return out, nil
	}
	root, err := os.OpenRoot(out.Path)
	if err != nil {
		return out, &InputError{"This Bud’s workspace is unavailable."}
	}
	defer root.Close()
	entries, _, err := workspaceEntries(root)
	if err != nil {
		return out, &InputError{"Couldn't read this Bud’s workspace."}
	}
	slices.SortFunc(entries, func(a, b workspaceEntry) int { return b.Modified.Compare(a.Modified) })
	out.Entries = entries[:min(len(entries), 12)]
	return out, nil
}

func workspaceEntries(root *os.Root) ([]workspaceEntry, bool, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	// Keep directory inspection bounded even when the agent works in a large repository.
	entries, err := dir.ReadDir(513)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	var visible []workspaceEntry
	for _, entry := range entries[:min(512, len(entries))] {
		if !validWorkspaceItemPath(entry.Name()) || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			visible = append(visible, workspaceEntry{entry.Name(), info.IsDir(), info.ModTime()})
		}
	}
	return visible, len(entries) > 512, nil
}

func scanAgentRefs(rows *sql.Rows, includeAppearance bool) ([]AgentRef, error) {
	defer rows.Close()
	var agents []AgentRef
	for rows.Next() {
		var a AgentRef
		fields := []any{&a.ID, &a.Name, &a.Role, &a.ConversationID, &a.UnreadThrough}
		var look Avatar
		var picture []byte
		if includeAppearance {
			fields = append(fields, &look.Color, &look.Shape, &look.Animate, &picture)
		}
		if err := rows.Scan(fields...); err != nil {
			return nil, err
		}
		a.HasNewResult = a.UnreadThrough != 0
		if includeAppearance {
			a.Avatar = &look
			// The directory is polled often. Fetch custom images separately
			// only when the saved picture has changed.
			if len(picture) != 0 {
				hash := sha256.Sum256(picture)
				a.PictureRevision = hex.EncodeToString(hash[:])
			}
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// Keep any unread background results from older homes. Ordinary replies start
// counting when the home first opens with chat unread tracking.
const unreadActivity = `COALESCE((SELECT max(m.id) FROM chat_turns ct
	JOIN messages m ON m.task_id=ct.task_id AND m.role='assistant' LEFT JOIN turn_details td ON td.task_id=ct.task_id
	WHERE ct.conversation_id=c.id AND m.id>COALESCE((SELECT through_id FROM background_reads WHERE conversation_id=c.id),0)
		AND (m.id>(SELECT through_id FROM chat_unread_baseline WHERE id=1)
			OR td.origin IN ('handoff','routine','work_input','watch','watch_notice'))),0)`

func (s *Store) MarkResultsRead(ctx context.Context, conversation string, through int64) error {
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO background_reads(conversation_id,through_id)
		SELECT ct.conversation_id,max(m.id) FROM chat_turns ct
		JOIN messages m ON m.task_id=ct.task_id AND m.role='assistant'
		WHERE ct.conversation_id=? AND m.id<=?
		GROUP BY ct.conversation_id ON CONFLICT(conversation_id) DO UPDATE
		SET through_id=max(background_reads.through_id,excluded.through_id)`, conversation, through); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE standing_inbox SET seen=1 WHERE conversation=? AND state IN ('notified','overflow')
		AND EXISTS(SELECT 1 FROM messages m WHERE m.task_id=standing_inbox.task_id AND m.role='assistant' AND m.id<=?)`,
		conversation, through); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Directory(ctx context.Context) (Directory, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Directory{}, err
	}
	defer tx.Rollback()
	var out Directory
	if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM workspace_primary WHERE id=1").Scan(&out.PrimaryAgentID); err != nil {
		return Directory{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM standing_inbox i LEFT JOIN tasks t ON t.id=i.task_id
		WHERE i.seen=0 AND (i.state='approval' OR t.status IN ('needs_input','succeeded','failed','interrupted'))`).
		Scan(&out.AutonomyUnread); err != nil {
		return Directory{}, err
	}
	if out.Skills, err = listSkills(ctx, tx); err != nil {
		return Directory{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,COALESCE(ar.role,''),c.id,`+unreadActivity+`,
		COALESCE(p.color,'ink'),COALESCE(p.shape,'orb'),COALESCE(p.animate,1),p.image
		FROM agents a JOIN conversations c ON c.agent_id=a.id LEFT JOIN agent_roles ar ON ar.agent_id=a.id
		LEFT JOIN agent_appearance p ON p.agent_id=a.id
		LEFT JOIN group_chats g ON g.conversation_id=c.id WHERE g.conversation_id IS NULL
		AND NOT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=a.id)
		ORDER BY (a.id=?) DESC,a.name COLLATE NOCASE,a.id`, out.PrimaryAgentID)
	if err != nil {
		return Directory{}, err
	}
	out.Agents, err = scanAgentRefs(rows, true)
	if err != nil {
		return Directory{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT a.id,a.name,COALESCE(ar.role,''),c.id,0,
		COALESCE(p.color,'ink'),COALESCE(p.shape,'orb'),COALESCE(p.animate,1),p.image
		FROM removed_buds b JOIN agents a ON a.id=b.agent_id JOIN conversations c ON c.agent_id=a.id
		LEFT JOIN agent_roles ar ON ar.agent_id=a.id LEFT JOIN agent_appearance p ON p.agent_id=a.id
		LEFT JOIN group_chats g ON g.conversation_id=c.id
		WHERE g.conversation_id IS NULL ORDER BY a.name COLLATE NOCASE,a.id`)
	if err != nil {
		return Directory{}, err
	}
	out.RemovedAgents, err = scanAgentRefs(rows, true)
	if err != nil {
		return Directory{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT c.id,g.name,`+unreadActivity+`,EXISTS(SELECT 1 FROM archived_groups a WHERE a.conversation_id=c.id)
		FROM group_chats g JOIN conversations c ON c.id=g.conversation_id ORDER BY g.rowid DESC`)
	if err != nil {
		return Directory{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var group ChatRef
		var archived bool
		if err := rows.Scan(&group.ID, &group.Name, &group.UnreadThrough, &archived); err != nil {
			return Directory{}, err
		}
		group.HasNewResult = group.UnreadThrough != 0
		if archived {
			out.ArchivedGroups = append(out.ArchivedGroups, group)
		} else {
			out.Groups = append(out.Groups, group)
		}
	}
	if err := rows.Err(); err != nil {
		return Directory{}, err
	}
	return out, tx.Commit()
}

func (s *Store) SetPrimaryAgent(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setPrimaryAgent(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func setPrimaryAgent(ctx context.Context, db writeQueryer, id string) error {
	var previous string
	if err := db.QueryRowContext(ctx, "SELECT agent_id FROM workspace_primary WHERE id=1").Scan(&previous); err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE workspace_primary SET agent_id=? WHERE id=1
		AND EXISTS (SELECT 1 FROM agents WHERE id=?)
		AND NOT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=?)`, id, id, id)
	if err := requireLeadershipUpdate(result, err, "Choose a Bud from your workspace."); err != nil {
		return err
	}
	if previous == id {
		return nil
	}
	return recordTeamEvent(ctx, db, "primary_changed", id, "")
}

func (s *Store) SetGroupLead(ctx context.Context, conversation, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setGroupLead(ctx, tx, conversation, id); err != nil {
		return err
	}
	return tx.Commit()
}

func setGroupLead(ctx context.Context, db writeQueryer, conversation, id string) error {
	var previous string
	if err := db.QueryRowContext(ctx, "SELECT agent_id FROM conversations WHERE id=?", conversation).Scan(&previous); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &InputError{"Choose a Bud in this group chat."}
		}
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE conversations SET agent_id=? WHERE id=?
		AND EXISTS (SELECT 1 FROM group_members WHERE conversation_id=? AND agent_id=?)
		AND NOT EXISTS (SELECT 1 FROM archived_groups WHERE conversation_id=conversations.id)`,
		id, conversation, conversation, id)
	if err := requireLeadershipUpdate(result, err, "Choose a Bud in this group chat."); err != nil {
		return err
	}
	if previous == id {
		return nil
	}
	return recordTeamEvent(ctx, db, "lead_changed", id, conversation)
}

func requireLeadershipUpdate(result sql.Result, err error, message string) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return &InputError{message}
	}
	return err
}

func groupMembers(ctx context.Context, db queryer, id string) ([]AgentRef, error) {
	rows, err := db.QueryContext(ctx, `SELECT a.id,a.name,COALESCE(ar.role,''),'',0
		FROM group_members gm JOIN agents a ON a.id=gm.agent_id LEFT JOIN agent_roles ar ON ar.agent_id=a.id
		WHERE gm.conversation_id=? ORDER BY gm.position`, id)
	if err != nil {
		return nil, err
	}
	return scanAgentRefs(rows, false)
}

func (s *Store) CreateGroup(ctx context.Context, req GroupRequest) (ChatRef, error) {
	req.Name = strings.TrimSpace(req.Name)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChatRef{}, err
	}
	defer tx.Rollback()
	chat, err := createGroup(ctx, tx, req)
	if err != nil {
		return ChatRef{}, err
	}
	return chat, tx.Commit()
}

func createGroup(ctx context.Context, tx *sql.Tx, req GroupRequest) (ChatRef, error) {
	if err := validateGroupRequest(req); err != nil {
		return ChatRef{}, err
	}
	for i, id := range req.Members {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a WHERE a.id=?
			AND NOT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=a.id))`, id).Scan(&exists); err != nil {
			return ChatRef{}, err
		}
		if !exists || slices.Contains(req.Members[:i], id) {
			return ChatRef{}, &InputError{"Select distinct Buds from your workspace."}
		}
	}
	lead := strings.TrimSpace(req.LeadAgentID)
	if lead == "" {
		var primary string
		if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM workspace_primary WHERE id=1").Scan(&primary); err != nil {
			return ChatRef{}, err
		}
		lead = req.Members[0]
		if slices.Contains(req.Members, primary) {
			lead = primary
		}
	} else if !slices.Contains(req.Members, lead) {
		return ChatRef{}, &InputError{"Choose a Lead Bud from the group members."}
	}
	chat := ChatRef{ID: "conv_" + rand.Text(), Name: req.Name, LeadAgentID: lead}
	if _, err := tx.ExecContext(ctx, "INSERT INTO conversations VALUES (?,?)", chat.ID, lead); err != nil {
		return ChatRef{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO group_chats VALUES (?,?)", chat.ID, chat.Name); err != nil {
		return ChatRef{}, err
	}
	for i, id := range req.Members {
		if _, err := tx.ExecContext(ctx, "INSERT INTO group_members VALUES (?,?,?)", chat.ID, id, i); err != nil {
			return ChatRef{}, err
		}
		if err := recordTeamEvent(ctx, tx, "joined_group", id, chat.ID); err != nil {
			return ChatRef{}, err
		}
	}
	return chat, nil
}

func validateGroupRequest(req GroupRequest) error {
	if !validTitle(req.Name, 100) || len(req.Members) < 2 || len(req.Members) > 8 {
		return &InputError{"Give the group a name and select 2 to 8 Buds."}
	}
	return nil
}

func conversationParticipant(ctx context.Context, db queryer, conversation, agent string) (Agent, error) {
	var a Agent
	var err error
	if agent == "" {
		a, err = scanAgent(db.QueryRowContext(ctx, conversationAgent, conversation))
	} else {
		a, err = scanAgent(db.QueryRowContext(ctx, `SELECT `+agentFields+` FROM agents a`+agentSettings+`
			JOIN conversations c ON c.id=? WHERE a.id=? AND (c.agent_id=a.id OR EXISTS(
				SELECT 1 FROM group_members gm WHERE gm.conversation_id=c.id AND gm.agent_id=a.id))`, conversation, agent))
		if errors.Is(err, ErrNotFound) {
			return Agent{}, &InputError{"Choose a Bud who belongs to this chat."}
		}
	}
	if err == nil && a.Removed {
		return Agent{}, &InputError{"This Bud was removed."}
	}
	return a, err
}

func validTitle(text string, limit int) bool {
	return text != "" && len(text) <= limit && !strings.ContainsAny(text, "\r\n\x00")
}

// RelocateManagedWorkspaces updates workspace references after a directory
// rename. Queued jobs already carry a snapshot of their workspace.
func (s *Store) RelocateManagedWorkspaces(ctx context.Context, oldRoot, newRoot string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prefix := oldRoot + string(filepath.Separator)
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET workspace=? || substr(workspace,length(?)+1)
		WHERE workspace=? OR substr(workspace,1,length(?))=?`,
		newRoot, oldRoot, oldRoot, prefix, prefix); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET repo=? || substr(repo,length(?)+1)
		WHERE kind IN (?,?,?) AND status IN ('queued','needs_input') AND (repo=? OR substr(repo,1,length(?))=?)`,
		newRoot, oldRoot, AgentTurn, BackgroundWork, RepoCheck, oldRoot, prefix, prefix); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE file_reference_workspaces SET workspace=? || substr(workspace,length(?)+1)
		WHERE workspace=? OR substr(workspace,1,length(?))=?`, newRoot, oldRoot, oldRoot, prefix, prefix); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE managed_workspace_root SET path=? WHERE id=1 AND path=?", newRoot, oldRoot); err != nil {
		return err
	}
	return tx.Commit()
}
