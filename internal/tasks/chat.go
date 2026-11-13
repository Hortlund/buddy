package tasks

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"buddy/internal/references"
)

const (
	AgentTurn             = "agent_turn"
	backgroundRequest     = "background_request"
	DefaultAgentID        = "agt_buddy"
	DefaultConversationID = "conv_buddy"
	DefaultCodexModel     = "gpt-6.1-terra"
)

const chatSchema = `
CREATE TABLE IF NOT EXISTS agents (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	instructions TEXT NOT NULL,
	workspace TEXT NOT NULL,
	allow_edits INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_appearance (
	agent_id TEXT PRIMARY KEY REFERENCES agents(id),
	color TEXT NOT NULL,
	face TEXT NOT NULL,
	finish TEXT NOT NULL DEFAULT 'satin',
	animate INTEGER NOT NULL,
	image BLOB
);
CREATE TABLE IF NOT EXISTS agent_models (
	agent_id TEXT PRIMARY KEY REFERENCES agents(id),
	model TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS conversations (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id)
);
CREATE TABLE IF NOT EXISTS chat_turns (
	task_id TEXT PRIMARY KEY REFERENCES tasks(id),
	conversation_id TEXT NOT NULL REFERENCES conversations(id),
	agent_name TEXT NOT NULL,
	instructions TEXT NOT NULL,
	allow_edits INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS turn_models (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	model TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS github_chat_results (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	result TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chat_connection_cards (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	card TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chat_watch_drafts (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	drafts TEXT NOT NULL,
	activated INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS steering_messages (
	id INTEGER PRIMARY KEY,
	task_id TEXT NOT NULL REFERENCES chat_turns(task_id),
	request_key TEXT NOT NULL UNIQUE,
	text TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('pending','applied','missed'))
);
CREATE INDEX IF NOT EXISTS steering_task ON steering_messages(task_id,id);
CREATE INDEX IF NOT EXISTS turns_conversation ON chat_turns(conversation_id);
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY,
	task_id TEXT NOT NULL REFERENCES chat_turns(task_id),
	role TEXT NOT NULL CHECK (role IN ('user','assistant')),
	text TEXT NOT NULL,
	created INTEGER NOT NULL,
	UNIQUE(task_id, role)
);
INSERT OR IGNORE INTO agents VALUES ('agt_buddy','Buddy','Help me with projects and everyday tasks. Be direct, practical, and concise.','',0);
INSERT OR IGNORE INTO conversations VALUES ('conv_buddy','agt_buddy');
`

type Agent struct {
	Removed              bool    `json:"removed,omitempty"`
	WorkspaceNeedsReview bool    `json:"workspace_needs_review,omitempty"`
	ConfirmWorkspace     bool    `json:"confirm_workspace,omitempty"`
	ID                   string  `json:"id"`
	ConversationID       string  `json:"conversation_id,omitempty"`
	Name                 string  `json:"name"`
	Role                 string  `json:"role,omitempty"`
	Instructions         string  `json:"instructions"`
	Workspace            string  `json:"workspace"`
	AllowEdits           bool    `json:"allow_edits"`
	Model                string  `json:"model,omitempty"`
	Avatar               *Avatar `json:"avatar,omitempty"`
}

type Message struct {
	ID                 int64             `json:"id"`
	TaskID             string            `json:"task_id"`
	Role               string            `json:"role"`
	Text               string            `json:"text"`
	At                 time.Time         `json:"at"`
	Status             Status            `json:"status,omitempty"`
	Activity           string            `json:"activity,omitempty"`
	DidWork            bool              `json:"did_work,omitempty"`
	Steering           []SteeringMessage `json:"steering,omitempty"`
	AgentID            string            `json:"agent_id,omitempty"`
	AgentName          string            `json:"agent_name,omitempty"`
	Origin             string            `json:"origin,omitempty"`
	Title              string            `json:"title,omitempty"`
	Card               *ChoiceCard       `json:"card,omitempty"`
	BackgroundQuestion bool              `json:"background_question,omitempty"`
	AnswerTo           string            `json:"answer_to,omitempty"`
	Attachments        []Attachment      `json:"attachments,omitempty"`
	GeneratedImage     bool              `json:"generated_image,omitempty"`
	Reactions          []MessageReaction `json:"reactions,omitempty"`
	ReactionRevision   int64             `json:"reaction_revision,omitempty"`
	JobID              string            `json:"job_id,omitempty"`
	JobTitle           string            `json:"job_title,omitempty"`
	JobConversationID  string            `json:"job_conversation_id,omitempty"`
	SkillName          string            `json:"skill_name,omitempty"`
	BudProposal        *BudProposal      `json:"bud_proposal,omitempty"`
	GitHub             *GitHubResult     `json:"github,omitempty"`
	ConnectionCard     *ConnectionCard   `json:"connection_card,omitempty"`
}

type Conversation struct {
	ID                      string           `json:"id"`
	Archived                bool             `json:"archived,omitempty"`
	TranscriptRevision      int64            `json:"transcript_revision,omitempty"`
	Agent                   Agent            `json:"agent"`
	Messages                []Message        `json:"messages"`
	Before                  int64            `json:"before,omitempty"`
	Active                  *ActiveRun       `json:"active,omitempty"`
	EarlierUpdates          []Message        `json:"earlier_updates,omitempty"`
	OpenBackgroundQuestions []string         `json:"open_background_questions"`
	ReactionOptions         []ReactionOption `json:"reaction_options,omitempty"`
	ReactionRevision        int64            `json:"reaction_revision"`
	ReactionUpdates         []ReactionUpdate `json:"reaction_updates,omitempty"`
	Name                    string           `json:"name,omitempty"`
	Members                 []AgentRef       `json:"members,omitempty"`
	Profiles                []Agent          `json:"profiles,omitempty"`
	Work                    []WorkItem       `json:"work,omitempty"`
	Routines                []Routine        `json:"routines,omitempty"`
}

type Turn struct {
	ExecutionPlan
	ControlForward  func(context.Context, json.RawMessage) (json.RawMessage, error)
	OutputDirectory string
	Projects        []Project

	Skill              *SkillInvocation
	WatchSignal        *StandingSignal
	Memories           []MemoryNote
	Soul               string
	soulRevision       string
	LearnedMemory      string
	CanLearn           bool
	Language           string
	WorkDetails        *WorkDetails
	Agent              Agent
	Messages           []Message
	steering           *turnSteering
	Origin             string
	Title              string
	Brief              string
	TeamStep           string
	Team               []AgentRef
	Buds               []AgentRef
	BuddyTeam          TeamContext
	Work               []WorkItem
	coordinate         bool
	revisingBud        bool
	routing            bool
	workspaceRoot      string
	action             *TurnAction
	Routines           []routineSummary
	Personality        string
	Devices            []Device
	DeviceExecution    bool
	CanRequestInput    bool
	Attachments        []Attachment
	imageTool          func(context.Context, ImageRequest) ([]byte, error)
	attachmentSource   string
	earlierAttachments bool
	github             GitHubChatReader
	githubConnections  []GitHubChatConnection
	gitCheckout        *gitCheckout
	integrations       ChatIntegrations
	connections        []ProviderConnection
	watches            []Watch
}

type AgentRunner func(context.Context, Turn, Emit) (string, error)

const agentFields = `a.id,a.name,a.instructions,a.workspace,a.allow_edits,COALESCE(am.model,'` + DefaultCodexModel + `'),
	COALESCE(p.color,'ink'),COALESCE(p.shape,'orb'),COALESCE(p.face,'smile'),COALESCE(p.finish,'satin'),COALESCE(p.animate,1),p.image,COALESCE(ar.role,''),
	EXISTS(SELECT 1 FROM restored_workspace_agents h WHERE h.agent_id=a.id),
	EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=a.id)`

const agentSettings = ` LEFT JOIN agent_appearance p ON p.agent_id=a.id LEFT JOIN agent_models am ON am.agent_id=a.id LEFT JOIN agent_roles ar ON ar.agent_id=a.id`

func scanAgent(row scanner) (Agent, error) {
	var a Agent
	var avatar Avatar
	err := row.Scan(&a.ID, &a.Name, &a.Instructions, &a.Workspace, &a.AllowEdits, &a.Model,
		&avatar.Color, &avatar.Shape, &avatar.Face, &avatar.Finish, &avatar.Animate, &avatar.Image, &a.Role, &a.WorkspaceNeedsReview, &a.Removed)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		a.Avatar = &avatar
	}
	return a, err
}

func (s *Store) Agent(ctx context.Context, id string) (Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentFields+` FROM agents a`+agentSettings+` WHERE a.id=?`, id))
}

func (s *Store) SaveAgent(ctx context.Context, id string, a Agent) (Agent, error) {
	return s.saveAgent(ctx, id, a, false)
}

func (s *Store) CreateAgent(ctx context.Context, a Agent) (Agent, error) {
	if a.Workspace != "" {
		return Agent{}, &InputError{"New Buds start in their own workspace. You can change it in Bud settings."}
	}
	return s.saveAgent(ctx, "agt_"+rand.Text(), a, true)
}

func (s *Store) saveAgent(ctx context.Context, id string, a Agent, create bool) (Agent, error) {
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	saved, err := s.saveAgentInTx(ctx, tx, id, a, create, false)
	if err != nil {
		return Agent{}, err
	}
	return saved, tx.Commit()
}

func (s *Store) saveAgentInTx(ctx context.Context, tx *sql.Tx, id string, a Agent, create, staged bool) (Agent, error) {
	a.ID, a.Name = id, strings.TrimSpace(a.Name)
	a.Model = strings.TrimSpace(a.Model)
	a.Role = strings.TrimSpace(a.Role)
	if a.Role != "" && !validTitle(a.Role, 100) {
		return Agent{}, &InputError{"Keep the role to one line under 100 bytes."}
	}
	if a.Model != "" && !validModel(a.Model) {
		return Agent{}, &InputError{"Enter a valid Codex model ID."}
	}
	if !validTitle(a.Name, 100) || len(a.Instructions) > 8000 {
		return Agent{}, &InputError{"Enter a name and keep the instructions under 8,000 bytes."}
	}
	if a.Avatar != nil {
		if err := validateAvatar(*a.Avatar); err != nil {
			return Agent{}, err
		}
	}
	var priorSoul string
	if !create {
		saved, err := scanAgent(tx.QueryRowContext(ctx, "SELECT "+agentFields+" FROM agents a"+agentSettings+" WHERE a.id=?", id))
		if err != nil {
			return Agent{}, err
		}
		if saved.WorkspaceNeedsReview && !a.ConfirmWorkspace {
			return Agent{}, &InputError{WorkspaceReviewMessage}
		}
		if saved.Removed {
			return Agent{}, &InputError{"This Bud was removed."}
		}
		priorSoul = initialSoul(saved.Name, saved.Role, saved.Instructions)
	}
	if a.Workspace != "" {
		if !filepath.IsAbs(a.Workspace) {
			return Agent{}, &InputError{"Choose an absolute working folder on the server."}
		}
		workspace, err := filepath.EvalSymlinks(a.Workspace)
		if err != nil {
			return Agent{}, &InputError{"That working folder is unavailable on the server."}
		}
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			return Agent{}, &InputError{"Choose a folder for this Bud’s workspace."}
		}
		a.Workspace = workspace
	} else if s.workspaceRoot != "" {
		if staged {
			a.Workspace = filepath.Join(s.workspaceRoot, id)
		} else {
			workspace, err := s.managedWorkspace(id)
			if err != nil {
				return Agent{}, &budProvisionError{err}
			}
			a.Workspace = workspace
		}
		if create {
			a.AllowEdits = true
		}
	} else if a.AllowEdits {
		return Agent{}, &InputError{"Choose a working folder before enabling edits."}
	}
	if create && !staged {
		if _, err := s.personality(id); err != nil {
			return Agent{}, &budProvisionError{err}
		}
	}
	conversationID := ""
	if create {
		conversationID = "conv_" + strings.TrimPrefix(id, "agt_")
		if _, err := tx.ExecContext(ctx, "INSERT INTO agents VALUES (?,?,?,?,?)", id, a.Name, a.Instructions, a.Workspace, a.AllowEdits); err != nil {
			return Agent{}, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO conversations VALUES (?,?)", conversationID, id); err != nil {
			return Agent{}, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bud_souls VALUES (?,?)", id, initialSoul(a.Name, a.Role, a.Instructions)); err != nil {
			return Agent{}, err
		}
	} else if next := initialSoul(a.Name, a.Role, a.Instructions); next != priorSoul {
		if _, err := tx.ExecContext(ctx, "UPDATE bud_souls SET body=? WHERE agent_id=? AND body=?", next, id, priorSoul); err != nil {
			return Agent{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_roles VALUES (?,?) ON CONFLICT(agent_id)
		DO UPDATE SET role=excluded.role`, id, a.Role); err != nil {
		return Agent{}, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE agents SET name=?,instructions=?,workspace=?,allow_edits=? WHERE id=?
		AND NOT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=agents.id)`,
		a.Name, a.Instructions, a.Workspace, a.AllowEdits, id)
	if err != nil {
		return Agent{}, err
	}
	if n, err := r.RowsAffected(); err != nil || n == 0 {
		if err == nil {
			err = ErrNotFound
		}
		return Agent{}, err
	}
	if !create && a.ConfirmWorkspace {
		if err := confirmRestoredWorkspace(ctx, tx, a); err != nil {
			return Agent{}, err
		}
	}
	if a.Avatar != nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_appearance(agent_id,color,face,finish,animate,image,shape)
			VALUES (?,?,COALESCE(NULLIF(?,''),'smile'),COALESCE(NULLIF(?,''),'satin'),?,?,COALESCE(NULLIF(?,''),'orb'))
			ON CONFLICT(agent_id) DO UPDATE SET color=excluded.color, face=excluded.face, finish=excluded.finish,
			animate=excluded.animate, image=excluded.image,
			shape=CASE WHEN ?='' THEN agent_appearance.shape ELSE excluded.shape END`,
			id, a.Avatar.Color, a.Avatar.Face, a.Avatar.Finish, a.Avatar.Animate, a.Avatar.Image, a.Avatar.Shape, a.Avatar.Shape)
		if err != nil {
			return Agent{}, err
		}
	}
	if a.Model != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_models(agent_id,model) VALUES (?,?)
			ON CONFLICT(agent_id) DO UPDATE SET model=excluded.model`, id, a.Model); err != nil {
			return Agent{}, err
		}
	}
	a, err = scanAgent(tx.QueryRowContext(ctx, `SELECT `+agentFields+` FROM agents a`+agentSettings+` WHERE a.id=?`, id))
	if err != nil {
		return Agent{}, err
	}
	a.ConversationID = conversationID
	if create || initialSoul(a.Name, a.Role, a.Instructions) != priorSoul {
		kind := "profile_updated"
		if create {
			kind = "joined_team"
		}
		if err := recordTeamEvent(ctx, tx, kind, id, ""); err != nil {
			return Agent{}, err
		}
	}
	return a, nil
}

const conversationAgent = `SELECT ` + agentFields + `
	FROM conversations c JOIN agents a ON a.id=c.agent_id` + agentSettings + ` WHERE c.id=?`

const visibleChatMessage = visibleTranscript + ` AND (m.role<>'user' OR NOT EXISTS(
	SELECT 1 FROM choice_cards q WHERE q.answer_task_id=m.task_id AND
	(EXISTS(SELECT 1 FROM management_confirmations mc WHERE mc.task_id=q.task_id)
	OR EXISTS(SELECT 1 FROM github_write_cards gw WHERE gw.task_id=q.task_id))))`

const messageFields = `m.id,m.task_id,m.role,m.text,m.created,t.status,t.summary,
	EXISTS(SELECT 1 FROM events e WHERE e.task_id=m.task_id AND e.stream='activity'),
	CASE WHEN m.role='user' OR t.kind='background_work' THEN ` + steeringJSON + ` ELSE '[]' END,
	COALESCE(td.agent_id,''),ct.agent_name,COALESCE(td.origin,'chat'),COALESCE(td.title,''),
	m.role='assistant' AND EXISTS(SELECT 1 FROM generated_images g WHERE g.task_id=t.id),
	CASE WHEN m.role='assistant' THEN COALESCE((SELECT json_object('question',q.question,'options',json(q.options),
		'choice',CASE WHEN ` + retryableChoice + ` THEN NULL ELSE q.choice END,
		'detail',d.detail,'option_details',json(d.option_details),
		'confirmation',COALESCE(mc.style,CASE WHEN gw.task_id IS NOT NULL THEN 'github' ELSE '' END))
		FROM choice_cards q LEFT JOIN choice_card_details d ON d.task_id=q.task_id
		LEFT JOIN management_confirmations mc ON mc.task_id=q.task_id
		LEFT JOIN github_write_cards gw ON gw.task_id=q.task_id WHERE q.task_id=t.id),'null') ELSE 'null' END,
	m.role='assistant' AND EXISTS(SELECT 1 FROM background_clarifications q WHERE q.task_id=t.id AND ` + openBackgroundQuestion + `),
	CASE WHEN m.role='user' THEN COALESCE((SELECT q.question FROM choice_card_answers a
		JOIN choice_cards q ON q.task_id=a.question_task_id WHERE a.answer_task_id=t.id),
		(SELECT question.text FROM background_clarification_answers q JOIN messages question ON question.task_id=q.question_task_id
		WHERE q.answer_task_id=t.id AND question.role='assistant'),
		(SELECT question.text FROM messages question WHERE t.kind='work_input' AND question.task_id=t.id AND question.role='assistant'),'') ELSE '' END,
	CASE WHEN m.role='user' THEN (SELECT json_group_array(json_object('name',name,'type',media_type,'size',size))
	FROM (SELECT name,media_type,length(data) AS size FROM chat_attachments f WHERE f.task_id=t.id ORDER BY position)) ELSE '[]' END,
	CASE WHEN m.role='user' OR t.kind='work_input' THEN COALESCE((SELECT job_id FROM work_questions WHERE task_id=t.id),'') ELSE '' END,
	CASE WHEN m.role='user' OR t.kind='work_input' THEN COALESCE((SELECT j.title FROM work_questions q JOIN turn_details j ON j.task_id=q.job_id
		WHERE q.task_id=t.id),'') ELSE '' END,
	CASE WHEN m.role='user' OR t.kind='work_input' THEN COALESCE((SELECT j.conversation_id FROM work_questions q JOIN chat_turns j ON j.task_id=q.job_id
		WHERE q.task_id=t.id),'') ELSE '' END,
	CASE WHEN m.role='user' THEN COALESCE((SELECT name FROM turn_skills WHERE task_id=t.id),'') ELSE '' END,
	CASE WHEN m.role='assistant' THEN COALESCE((SELECT json_object('name',p.name,'role',p.role,'instructions',p.instructions,
		'needs_revision',json(CASE WHEN EXISTS(SELECT 1 FROM bud_choice_cards b WHERE b.task_id=p.task_id) THEN 'true' ELSE 'false' END),
		'pending_work',json(CASE WHEN c.id IS NULL THEN (SELECT json_group_array(json_extract(j.value,'$.title'))
			FROM bud_proposal_work w,json_each(w.handoffs) j WHERE w.task_id=p.task_id) ELSE '[]' END),
		'conversation_id',COALESCE(c.id,'')) FROM bud_proposals p LEFT JOIN conversations c ON c.agent_id=p.bud_id
		AND c.id='conv_' || substr(p.bud_id,5) WHERE p.task_id=t.id),'null') ELSE 'null' END,
	CASE WHEN m.role='assistant' THEN COALESCE((SELECT result FROM github_chat_results WHERE task_id=t.id),'null') ELSE 'null' END,
	CASE WHEN m.role='assistant' THEN COALESCE((SELECT card FROM chat_connection_cards WHERE task_id=t.id),'null') ELSE 'null' END,
	COALESCE((SELECT revision FROM reaction_versions WHERE message_id=m.id),0),` + reactionJSON

func (s *Store) Conversation(ctx context.Context, id string, before int64) (Conversation, error) {
	return s.ConversationWithReactions(ctx, id, before, -1)
}

func (s *Store) ConversationWithReactions(ctx context.Context, id string, before, reactionsAfter int64) (Conversation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Conversation{}, err
	}
	defer tx.Rollback()
	agent, err := scanAgent(tx.QueryRowContext(ctx, conversationAgent, id))
	if err != nil {
		return Conversation{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT `+messageFields+` FROM messages m
		JOIN chat_turns ct ON ct.task_id=m.task_id JOIN tasks t ON t.id=m.task_id LEFT JOIN turn_details td ON td.task_id=t.id
		WHERE ct.conversation_id=? AND `+visibleChatMessage+` AND (?=0 OR m.id<?) ORDER BY m.id DESC LIMIT 121`, id, before, before)
	if err != nil {
		return Conversation{}, err
	}
	messages, err := scanMessages(rows)
	if err != nil {
		return Conversation{}, err
	}
	var next int64
	if len(messages) > 120 {
		messages = messages[:120]
		next = messages[len(messages)-1].ID
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	conversation := Conversation{ID: id, Agent: agent, Messages: messages, Before: next}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(revision),0) FROM reaction_versions WHERE conversation_id=?", id).
		Scan(&conversation.ReactionRevision); err != nil {
		return Conversation{}, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT revision FROM transcript_revisions WHERE conversation_id=?),0)", id).
		Scan(&conversation.TranscriptRevision); err != nil {
		return Conversation{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT name,EXISTS(SELECT 1 FROM archived_groups a WHERE a.conversation_id=g.conversation_id)
		FROM group_chats g WHERE conversation_id=?`, id).Scan(&conversation.Name, &conversation.Archived)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, err
	}
	if conversation.Name != "" {
		conversation.Members, err = groupMembers(ctx, tx, id)
		if err != nil {
			return Conversation{}, err
		}
		if before == 0 {
			for _, member := range conversation.Members {
				if member.ID == agent.ID {
					continue
				}
				profile, err := conversationParticipant(ctx, tx, id, member.ID)
				if err != nil {
					return Conversation{}, err
				}
				conversation.Profiles = append(conversation.Profiles, profile)
			}
		}
	}
	if before == 0 {
		conversation.ReactionOptions = reactionOptions
		conversation.OpenBackgroundQuestions, err = backgroundQuestions(ctx, tx, id)
		if err != nil {
			return Conversation{}, err
		}
		if reactionsAfter >= 0 && reactionsAfter < conversation.ReactionRevision && next != 0 {
			conversation.ReactionUpdates, err = reactionUpdates(ctx, tx, id, reactionsAfter, next)
			if err != nil {
				return Conversation{}, err
			}
		}
		if conversation.Work, err = listWork(ctx, tx, id); err != nil {
			return Conversation{}, err
		}
		if conversation.Routines, err = listRoutines(ctx, tx, id); err != nil {
			return Conversation{}, err
		}
		var active ActiveRun
		err := tx.QueryRowContext(ctx, `SELECT t.id,t.status,t.summary,
			COALESCE((SELECT e.text FROM events e WHERE e.task_id=t.id AND e.stream='commentary' ORDER BY e.seq DESC LIMIT 1),''),
			COALESCE(td.agent_id,?),ct.agent_name FROM tasks t
			JOIN chat_turns ct ON ct.task_id=t.id LEFT JOIN turn_details td ON td.task_id=t.id
			WHERE ct.conversation_id=? AND t.kind='agent_turn'
			AND t.status IN ('running','cancelling') ORDER BY t.rowid LIMIT 1`, agent.ID, id).
			Scan(&active.ID, &active.Status, &active.Activity, &active.Update, &active.AgentID, &active.AgentName)
		if err == nil {
			active.Kind = activityKind(active.Activity)
			if active.Status == Cancelling {
				active.Update = ""
			}
			conversation.Active = &active
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Conversation{}, err
		}
		if next != 0 {
			// Refresh older turns whose reply, choice, or draft approval
			// changed after a client loaded that part of the transcript.
			rows, err := tx.QueryContext(ctx, `
				SELECT `+messageFields+` FROM messages m
				JOIN chat_turns ct ON ct.task_id=m.task_id JOIN tasks t ON t.id=m.task_id LEFT JOIN turn_details td ON td.task_id=t.id
				WHERE ct.conversation_id=? AND `+visibleChatMessage+` AND m.id<? AND ((m.role='user'
				AND m.task_id IN (
					SELECT id FROM tasks WHERE status IN ('queued','running','cancelling')
					UNION ALL SELECT task_id FROM messages WHERE role='assistant' AND id>=?
				)) OR (m.role='assistant' AND EXISTS(SELECT 1 FROM choice_cards q JOIN messages answer
					ON answer.task_id=q.answer_task_id WHERE q.task_id=m.task_id
					AND (answer.id>=? OR (`+retryableChoice+`))))
				OR (m.role='assistant' AND ?>=0 AND EXISTS(
					SELECT 1 FROM reaction_versions v JOIN bud_proposal_approvals p ON p.task_id=m.task_id
					WHERE v.message_id=m.id AND v.revision>?)))
				ORDER BY m.id`, id, next, next, next, reactionsAfter, reactionsAfter)
			if err != nil {
				return Conversation{}, err
			}
			conversation.EarlierUpdates, err = scanMessages(rows)
			if err != nil {
				return Conversation{}, err
			}
		}
	}
	return conversation, tx.Commit()
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	messages := make([]Message, 0)
	for rows.Next() {
		var m Message
		var at int64
		var steering, card, attachments, proposal, github, connection, reactions string
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Role, &m.Text, &at, &m.Status, &m.Activity, &m.DidWork, &steering,
			&m.AgentID, &m.AgentName, &m.Origin, &m.Title, &m.GeneratedImage, &card, &m.BackgroundQuestion, &m.AnswerTo, &attachments, &m.JobID, &m.JobTitle,
			&m.JobConversationID, &m.SkillName, &proposal, &github, &connection, &m.ReactionRevision, &reactions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(steering), &m.Steering); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(card), &m.Card); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(attachments), &m.Attachments); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(proposal), &m.BudProposal); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(github), &m.GitHub); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(connection), &m.ConnectionCard); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(reactions), &m.Reactions); err != nil {
			return nil, err
		}
		m.At = time.UnixMicro(at).UTC()
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (s *Store) Send(ctx context.Context, conversation, text, key string) (Task, bool, error) {
	return s.SendTo(ctx, conversation, "", text, key)
}

func (s *Store) SendTo(ctx context.Context, conversation, target, text, key string) (Task, bool, error) {
	return s.SendWithAttachments(ctx, conversation, target, text, key, nil)
}

func (s *Store) SendWithAttachments(ctx context.Context, conversation, target, text, key string, files []Attachment) (Task, bool, error) {
	return s.SendWithWork(ctx, conversation, target, text, key, files, "")
}

func (s *Store) SendWithWork(ctx context.Context, conversation, target, text, key string, files []Attachment, jobID string) (Task, bool, error) {
	return s.SendMessage(ctx, conversation, key, MessageRequest{
		Text: text, AgentID: target, Attachments: files, JobID: jobID,
	})
}

type MessageRequest struct {
	Text             string       `json:"text"`
	AgentID          string       `json:"agent_id,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
	JobID            string       `json:"job_id,omitempty"`
	SkillName        string       `json:"skill_name,omitempty"`
	Background       bool         `json:"background,omitempty"`
	BackgroundAnswer string       `json:"background_answer,omitempty"`
}

func (s *Store) SendMessage(ctx context.Context, conversation, key string, input MessageRequest) (Task, bool, error) {
	files, err := PrepareAttachments(input.Attachments)
	if err != nil {
		return Task{}, false, err
	}
	text, target, jobID := strings.TrimSpace(input.Text), input.AgentID, input.JobID
	if (text == "" && len(files) == 0) || len(text) > 12000 {
		return Task{}, false, &InputError{"Write a message or attach a file. Messages can be up to 12,000 bytes."}
	}
	background := input.Background || input.BackgroundAnswer != ""
	if background && (jobID != "" || input.SkillName != "" ||
		input.BackgroundAnswer == "" && (text == "" || strings.HasPrefix(text, "/"))) {
		return Task{}, false, &InputError{"Write a standalone background assignment. Send slash commands and job replies in chat."}
	}
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return Task{}, false, err
	}
	defer tx.Rollback()
	if key == "" {
		key = rand.Text()
	}
	t, err := scanTask(tx.QueryRowContext(ctx,
		"SELECT "+fields+" FROM tasks WHERE kind=? AND request_key IN (?,?)", AgentTurn, namespacedKey(AgentTurn, key), "chat:"+key))
	if err == nil {
		var body, id, actual, defaultID, savedJob, savedSkill, origin, savedQuestion string
		var usedDefault bool
		if err := tx.QueryRowContext(ctx, `
			SELECT m.text,ct.conversation_id,COALESCE(td.agent_id,c.agent_id),c.agent_id,
			EXISTS(SELECT 1 FROM chat_default_recipients WHERE task_id=ct.task_id),
			COALESCE((SELECT job_id FROM work_questions WHERE task_id=ct.task_id),''),
			COALESCE((SELECT name FROM turn_skills WHERE task_id=ct.task_id),''), COALESCE(td.origin,'chat'),
			COALESCE((SELECT question_task_id FROM background_clarification_answers WHERE answer_task_id=ct.task_id),'')
			FROM messages m JOIN chat_turns ct ON ct.task_id=m.task_id
			JOIN conversations c ON c.id=ct.conversation_id LEFT JOIN turn_details td ON td.task_id=ct.task_id
			WHERE m.task_id=? AND m.role='user'`, t.ID).Scan(&body, &id, &actual, &defaultID, &usedDefault, &savedJob, &savedSkill, &origin, &savedQuestion); err != nil {
			return Task{}, false, err
		}
		// An omitted recipient uses the original recipient on retry, even
		// if the group lead changed since the message was accepted.
		if target == "" {
			target = defaultID
			if usedDefault {
				target = actual
			}
		}
		if body != text || id != conversation || target != actual || savedJob != jobID ||
			background != (origin == backgroundRequest) || input.BackgroundAnswer != savedQuestion || (input.SkillName != "" && input.SkillName != savedSkill) {
			return Task{}, false, ErrConflict
		}
		saved, err := readAttachments(ctx, tx, t.ID)
		if err != nil {
			return Task{}, false, err
		}
		if AttachmentDigest(saved) != AttachmentDigest(files) {
			return Task{}, false, ErrConflict
		}
		return t, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Task{}, false, err
	}
	if err := requireOpenConversation(ctx, tx, conversation); err != nil {
		return Task{}, false, err
	}
	usedDefault := target == ""
	if input.BackgroundAnswer != "" {
		var requester string
		var open bool
		err := tx.QueryRowContext(ctx, `SELECT td.agent_id,`+openBackgroundQuestion+` FROM background_clarifications q
			JOIN chat_turns ct ON ct.task_id=q.task_id JOIN turn_details td ON td.task_id=q.task_id
			WHERE q.task_id=? AND ct.conversation_id=?`, input.BackgroundAnswer, conversation).Scan(&requester, &open)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false, &InputError{"Choose an open background question from this chat."}
		}
		if err != nil {
			return Task{}, false, err
		}
		if !open {
			return Task{}, false, &InputError{"This background question has already been answered."}
		}
		if target != "" && target != requester {
			return Task{}, false, &InputError{"Reply to the Bud who asked the background question."}
		}
		target = requester
		if len(files) != 0 {
			inherited, err := readRequestAttachments(ctx, tx, input.BackgroundAnswer)
			if err != nil {
				return Task{}, false, err
			}
			if _, err := PrepareAttachments(append(inherited, files...)); err != nil {
				return Task{}, false, &InputError{"Background work can carry up to five files totaling 8 MB, including files you already attached."}
			}
		}
	}
	if usedDefault && jobID != "" {
		// A group can answer through the Bud assigned to the referenced job.
		// Outside workers aren't participants in the originating conversation.
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((
			SELECT td.agent_id FROM turn_details td
			JOIN group_members gm ON gm.agent_id=td.agent_id AND gm.conversation_id=?
			JOIN chat_turns ct ON ct.task_id=td.task_id AND ct.conversation_id=gm.conversation_id
			WHERE td.task_id=? AND NOT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=td.agent_id)
		),'')`, conversation, jobID).Scan(&target); err != nil {
			return Task{}, false, err
		}
	}
	agent, err := conversationParticipant(ctx, tx, conversation, target)
	if err != nil {
		return Task{}, false, err
	}
	origin := "chat"
	if background {
		origin = backgroundRequest
	}
	if err := checkQueue(ctx, tx, false); err != nil {
		return Task{}, false, err
	}
	t, err = enqueueTurn(ctx, tx, AgentTurn, key, conversation, agent, workRequest{origin: origin, backgroundQuestion: input.BackgroundAnswer})
	if err != nil {
		return Task{}, false, err
	}
	if input.BackgroundAnswer != "" {
		if _, err := tx.ExecContext(ctx, "INSERT INTO background_clarification_answers VALUES (?,?)", t.ID, input.BackgroundAnswer); err != nil {
			return Task{}, false, err
		}
	}
	if usedDefault {
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_default_recipients SELECT ?
			WHERE EXISTS(SELECT 1 FROM group_chats WHERE conversation_id=?)`, t.ID, conversation); err != nil {
			return Task{}, false, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO messages(task_id,role,text,created) VALUES (?,'user',?,?)",
		t.ID, text, t.CreatedAt.UnixMicro()); err != nil {
		return Task{}, false, err
	}
	if !background {
		if err := bindSkill(ctx, tx, t.ID, text, input.SkillName); err != nil {
			return Task{}, false, err
		}
	}
	if jobID != "" {
		result, err := tx.ExecContext(ctx, `INSERT INTO work_questions(task_id,job_id)
			SELECT ?,t.id FROM tasks t JOIN chat_turns ct ON ct.task_id=t.id JOIN turn_details td ON td.task_id=t.id
			WHERE t.id=? AND t.kind=? AND ct.conversation_id=?`, t.ID, jobID, BackgroundWork, conversation)
		if err != nil {
			return Task{}, false, err
		}
		if count, err := result.RowsAffected(); err != nil {
			return Task{}, false, err
		} else if count == 0 {
			return Task{}, false, &InputError{"Choose a background job from this chat."}
		}
	}
	for i, f := range files {
		if _, err := tx.ExecContext(ctx, "INSERT INTO chat_attachments VALUES (?,?,?,?,COALESCE(?,X''))", t.ID, i, f.Name, f.Type, f.Data); err != nil {
			return Task{}, false, err
		}
	}
	return t, true, tx.Commit()
}

func (s *Store) turn(ctx context.Context, task Task) (Turn, error) {
	var turn Turn
	var planErr error
	turn.ExecutionPlan, planErr = readExecutionPlan(ctx, s.db, task.ID)
	if planErr != nil {
		return turn, planErr
	}
	turn.Projects, planErr = s.Projects(ctx)
	if planErr != nil {
		return turn, planErr
	}
	var conv string
	turn.Agent.Workspace = task.Repository
	var parent string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(td.agent_id,c.agent_id),ct.conversation_id,ct.agent_name,
		ct.instructions,ct.allow_edits,COALESCE(tm.model,?),COALESCE(td.agent_role,''),COALESCE(td.origin,'chat'),
		COALESCE(td.title,''),COALESCE(td.brief,''),COALESCE(td.parent_id,'')
		FROM chat_turns ct JOIN conversations c ON c.id=ct.conversation_id
		LEFT JOIN turn_models tm ON tm.task_id=ct.task_id LEFT JOIN turn_details td ON td.task_id=ct.task_id WHERE ct.task_id=?`, DefaultCodexModel, task.ID).
		Scan(&turn.Agent.ID, &conv, &turn.Agent.Name, &turn.Agent.Instructions, &turn.Agent.AllowEdits, &turn.Agent.Model,
			&turn.Agent.Role, &turn.Origin, &turn.Title, &turn.Brief, &parent); err != nil {
		return Turn{}, err
	}
	if turn.Origin == backgroundRequest {
		turn.Agent.Workspace = ""
	}
	if turn.Origin == WatchOrigin {
		var raw string
		if err := s.db.QueryRowContext(ctx, "SELECT signal FROM standing_inbox WHERE task_id=?", task.ID).Scan(&raw); err != nil {
			return Turn{}, err
		}
		turn.WatchSignal = new(StandingSignal)
		if err := json.Unmarshal([]byte(raw), turn.WatchSignal); err != nil {
			return Turn{}, err
		}
		turn.Agent.AllowEdits, turn.Agent.Workspace = false, ""
		turn.CanRequestInput = true
		brief, err := backgroundBrief(ctx, s.db, task.ID, turn.Brief)
		turn.Brief = brief
		if err == nil {
			var languages LanguagePreferences
			languages, err = s.Languages(ctx)
			turn.Language = languages.Chat
		}
		return turn, err
	}
	var teamCall string
	var teamReleased int64
	var wrapUp bool
	err := s.db.QueryRowContext(ctx, `SELECT step.call_id,run.released_at,step.wrap_up FROM voice_team_steps step
		JOIN voice_team_runs run ON run.call_id=step.call_id WHERE step.task_id=?`, task.ID).Scan(&teamCall, &teamReleased, &wrapUp)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Turn{}, err
	}
	if teamReleased != 0 {
		turn.TeamStep = "contribute"
		if wrapUp {
			turn.TeamStep = "wrap_up"
		}
	}
	personality, err := s.personality(turn.Agent.ID)
	if err != nil {
		return Turn{}, err
	}
	turn.Personality = personality
	turn.Soul, turn.soulRevision, err = soulDocument(ctx, s.db, turn.Agent.ID)
	if err != nil {
		return Turn{}, err
	}
	if task.Kind == AgentTurn {
		bud, _, err := privateMemoryTurn(ctx, s.db, task.ID, true)
		if err != nil {
			return Turn{}, err
		}
		turn.CanLearn = bud != ""
		// Learning can be paused while previously learned context remains available.
		var private bool
		if err := s.db.QueryRowContext(ctx, "SELECT NOT EXISTS(SELECT 1 FROM group_chats WHERE conversation_id=?)", conv).Scan(&private); err != nil {
			return Turn{}, err
		}
		if private {
			turn.LearnedMemory, err = learnedDocument(ctx, s.db, turn.Agent.ID)
			if err != nil {
				return Turn{}, err
			}
		}
	}
	languages, err := s.Languages(ctx)
	if err != nil {
		return Turn{}, err
	}
	turn.Language = languages.Chat
	turn.Memories, err = s.StandingMemories(ctx, turn.Agent.ID)
	if err != nil {
		return Turn{}, err
	}
	turn.CanRequestInput = task.Kind == BackgroundWork
	turn.workspaceRoot = s.workspaceRoot
	turn.coordinate = task.Kind == AgentTurn && (turn.Origin == backgroundRequest || s.workspaceRoot != "" && turn.Origin != "peer_reply")
	if task.Kind == AgentTurn {
		turn.revisingBud, err = isBudRevision(ctx, s.db, task.ID)
		if err != nil {
			return Turn{}, err
		}
		turn.coordinate = turn.coordinate || turn.revisingBud
		if turn.revisingBud {
			turn.Agent.Workspace, turn.Agent.AllowEdits, turn.CanLearn = "", false, false
		}
	}
	if turn.Skill, err = skillForTurn(ctx, s.db, task.ID, parent); err != nil {
		return Turn{}, err
	}
	if turn.Team, err = groupMembers(ctx, s.db, conv); err != nil {
		return Turn{}, err
	}
	turn.BuddyTeam, err = s.TeamContext(ctx, conv, turn.Agent.ID)
	if err != nil {
		return Turn{}, err
	}
	turn.BuddyTeam.CurrentTaskID = task.ID
	if turn.coordinate {
		watches, err := s.Watches(ctx)
		if err != nil {
			return Turn{}, err
		}
		for _, watch := range watches {
			if watch.ConversationID == conv {
				turn.watches = append(turn.watches, watch)
			}
		}
		turn.Devices, err = s.Hosts(ctx)
		if err != nil {
			return Turn{}, err
		}
		if turn.Work, err = s.WorkContext(ctx, conv, turn.Agent.ID); err != nil {
			return Turn{}, err
		}
		directory, err := s.Directory(ctx)
		if err != nil {
			return Turn{}, err
		}
		for _, bud := range directory.Agents {
			turn.Buds = append(turn.Buds, AgentRef{ID: bud.ID, Name: bud.Name, Role: bud.Role})
		}
		routines, err := listRoutines(ctx, s.db, conv)
		if err != nil {
			return Turn{}, err
		}
		for _, routine := range routines {
			turn.Routines = append(turn.Routines, routineSummary{routine.ID, routine.Device, routine.Name, routine.AgentID, routine.Minutes, routine.NextAt, routine.Enabled})
		}
	}
	assignmentOnly := false
	if task.Kind == BackgroundWork {
		var participant bool
		err := s.db.QueryRowContext(ctx, `SELECT c.agent_id=? OR EXISTS(
			SELECT 1 FROM group_members gm WHERE gm.conversation_id=c.id AND gm.agent_id=?)
			FROM conversations c WHERE c.id=?`, turn.Agent.ID, turn.Agent.ID, conv).Scan(&participant)
		if err != nil {
			return Turn{}, err
		}
		assignmentOnly = !participant
	}
	cutoff := task.ID
	if parent != "" {
		cutoff = parent
	}
	// An answer can refer to a question long outside the normal transcript window.
	// Follow its question chain and reserve room for the originating request.
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE question_links(task_id,answer_task_id) AS (`+chatQuestionLinks+`), question_context(task_id) AS (
			SELECT task_id FROM question_links WHERE answer_task_id=?
			UNION SELECT q.task_id FROM question_links q JOIN question_context c ON q.answer_task_id=c.task_id
		), original_question(task_id) AS (
			SELECT t.id FROM tasks t JOIN question_context c ON c.task_id=t.id ORDER BY t.rowid LIMIT 1
		)
		SELECT `+messageFields+` FROM chat_turns ct JOIN tasks t ON t.id=ct.task_id JOIN messages m ON m.task_id=t.id
		LEFT JOIN turn_details td ON td.task_id=t.id
		WHERE ct.conversation_id=? AND `+visibleTranscript+`
			AND (NOT ? OR m.task_id=?)
			AND (?='' OR EXISTS (SELECT 1 FROM voice_call_turns vc WHERE vc.task_id=t.id AND vc.call_id=?)
				OR EXISTS (SELECT 1 FROM voice_team_steps vs WHERE vs.task_id=t.id AND vs.call_id=?))
			AND (t.rowid<=(SELECT rowid FROM tasks WHERE id=?)
			OR (? > 0 AND m.created<=?)
			OR (m.role='assistant' AND t.kind='background_work' AND m.created<=(SELECT started FROM tasks WHERE id=?)))
		ORDER BY CASE
			WHEN m.task_id=? AND m.role='user' THEN 3
			WHEN m.task_id=(SELECT task_id FROM original_question) AND m.role='user' THEN 2
			WHEN m.task_id IN (SELECT task_id FROM question_context) THEN 1
		ELSE 0 END DESC, m.id DESC LIMIT 40`, cutoff, conv, assignmentOnly, task.ID, teamCall, teamCall, teamCall, cutoff, teamReleased, teamReleased, task.ID, cutoff)
	if err != nil {
		return Turn{}, err
	}
	recent, err := scanMessages(rows)
	if err != nil {
		return Turn{}, err
	}
	if !turn.coordinate {
		recent = slices.DeleteFunc(recent, func(m Message) bool { return m.ConnectionCard != nil })
	}
	// Apply the context budget after prioritizing the request and any card context.
	size := 0
	for i, m := range recent {
		if m.Status == Cancelled || m.Status == Cancelling {
			recent[i].Attachments = nil
		}
		size += len(m.Text) + len(m.AnswerTo)
		if m.GitHub != nil {
			data, _ := json.Marshal(m.GitHub)
			size += len(data)
		}
		if m.ConnectionCard != nil {
			data, _ := json.Marshal(m.ConnectionCard.promptReference())
			size += len(data)
		}
		if m.Card != nil {
			size += len(m.Card.Question) + len(m.Card.Detail)
			for _, note := range m.Card.OptionDetails {
				size += len(note)
			}
			for _, option := range m.Card.Options {
				size += len(option)
			}
		}
		if m.BudProposal != nil {
			size += len(m.BudProposal.Name) + len(m.BudProposal.Role) + len(m.BudProposal.Instructions)
		}
		for _, correction := range m.Steering {
			if correction.State == steerApplied {
				size += len(correction.Text)
			}
		}
		if size > 64000 {
			recent = recent[:i]
			break
		}
	}
	var request *Message
	if len(recent) != 0 && recent[0].TaskID == cutoff && recent[0].Role == "user" {
		request = &recent[0]
		recent = recent[1:]
	}
	slices.SortFunc(recent, func(a, b Message) int { return cmp.Compare(a.ID, b.ID) })
	turn.Messages = recent
	if request != nil {
		turn.Messages = append(turn.Messages, *request)
	}
	switch {
	case parent != "":
		turn.attachmentSource, err = handoffAttachmentSource(ctx, s.db, task.ID, parent)
	case task.Kind == AgentTurn:
		turn.attachmentSource, err = requestAttachmentSource(ctx, s.db, cutoff)
	}
	if err == nil && turn.attachmentSource == "" && task.Kind == AgentTurn {
		for i := len(recent) - 1; i >= 0; i-- {
			if len(recent[i].Attachments) != 0 && recent[i].Status == Succeeded {
				turn.attachmentSource = recent[i].TaskID
				turn.earlierAttachments = true
				break
			}
		}
	}
	if err == nil && turn.CanRequestInput {
		turn.Brief, err = backgroundBrief(ctx, s.db, task.ID, turn.Brief)
		if err == nil {
			turn.Attachments, err = readWorkAttachments(ctx, s.db, task.ID, turn.attachmentSource)
		}
	} else if err == nil && turn.attachmentSource != "" {
		turn.Attachments, err = readRequestAttachments(ctx, s.db, turn.attachmentSource)
	}
	if err == nil && task.Kind == AgentTurn && request != nil && request.JobID != "" {
		// The persisted link authorizes this reference even if inviting a
		// Bud to the call moved the job since the question was sent.
		detail, workErr := s.WorkDetails(ctx, request.JobConversationID, request.JobID)
		if workErr == nil {
			detail.Events = detail.Events[max(0, len(detail.Events)-4):]
			detail.Steering = nil
			turn.WorkDetails = &detail
		} else if !errors.Is(workErr, ErrNotFound) {
			err = workErr
		}
	}
	return turn, err
}

func addReply(ctx context.Context, db writeQueryer, id, text string) error {
	if strings.Contains(text, "`") {
		var bud, workspace string
		err := db.QueryRowContext(ctx, `SELECT COALESCE(td.agent_id,c.agent_id),CASE WHEN EXISTS(SELECT 1 FROM device_runs dr WHERE dr.task_id=t.id) THEN ''
                ELSE COALESCE((SELECT f.workspace FROM file_reference_workspaces f
                WHERE f.task_id=t.id AND f.bud_id=COALESCE(td.agent_id,c.agent_id)),t.repo) END
            FROM tasks t JOIN chat_turns ct ON ct.task_id=t.id JOIN conversations c ON c.id=ct.conversation_id
			LEFT JOIN turn_details td ON td.task_id=t.id WHERE t.id=?`, id).Scan(&bud, &workspace)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		text = references.LinkFiles(text, bud, workspace, id)
	}
	artifacts, artifactErr := workArtifacts(ctx, db, id)
	if artifactErr != nil {
		return artifactErr
	}
	for _, artifact := range artifacts {
		if !strings.Contains(text, artifact.URL) {
			text += "\n\n[" + escapeTitle(artifact.Name) + "](" + artifact.URL + ")"
		}
	}
	if err := captureFileReferences(ctx, db, id, text); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO messages(task_id,role,text,created)
		SELECT task_id,'assistant',?,? FROM chat_turns WHERE task_id=?`, text, time.Now().UnixMicro(), id)
	return err
}
