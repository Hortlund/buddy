package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"
)

type GroupRouting struct {
	Lead     string        `json:"lead_bud_id"`
	Team     *TeamContext  `json:"buddy_team,omitempty"`
	Buds     []AgentRef    `json:"buds"`
	Recent   []routingLine `json:"recent"`
	Message  string        `json:"message"`
	model    string
	chat     string
	original string
}

type routingLine struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

const routeSchema = `{"type":"object","additionalProperties":false,"required":["agent_id"],"properties":{"agent_id":{"type":"string"}}}`

func (s *Store) groupRouting(ctx context.Context, task string) (*GroupRouting, error) {
	route := new(GroupRouting)
	err := s.db.QueryRowContext(ctx, `SELECT ct.conversation_id,td.agent_id,tm.model,m.text
		FROM chat_default_recipients d JOIN chat_turns ct ON ct.task_id=d.task_id
		JOIN turn_details td ON td.task_id=d.task_id JOIN turn_models tm ON tm.task_id=d.task_id
		JOIN messages m ON m.task_id=d.task_id AND m.role='user'
		WHERE d.task_id=? AND td.origin='chat'
		AND NOT EXISTS(SELECT 1 FROM work_questions w WHERE w.task_id=d.task_id)
		AND NOT EXISTS(SELECT 1 FROM archived_groups a WHERE a.conversation_id=ct.conversation_id)`, task).
		Scan(&route.chat, &route.Lead, &route.model, &route.Message)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	route.Buds, err = groupMembers(ctx, s.db, route.chat)
	if err != nil || len(route.Buds) < 2 {
		return nil, err
	}
	route.original = route.Lead
	team, err := s.TeamContext(ctx, route.chat, route.original)
	if err != nil {
		return nil, err
	}
	route.Team, route.Lead = &team, team.LeadID
	if len(route.Message) > 4000 {
		route.Message = managementExcerpt(route.Message, 4000)
	}
	return route, nil
}

func (s *Store) routingHistory(ctx context.Context, route *GroupRouting, task string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT CASE WHEN m.role='user' THEN 'User' ELSE ct.agent_name END,
		substr(m.text,1,400) FROM messages m JOIN chat_turns ct ON ct.task_id=m.task_id
		JOIN tasks t ON t.id=m.task_id LEFT JOIN turn_details td ON td.task_id=t.id
		WHERE ct.conversation_id=? AND m.id<(SELECT id FROM messages WHERE task_id=? AND role='user')
		AND `+visibleTranscript+` AND m.text<>'' AND td.origin IN ('chat','peer_reply') ORDER BY m.id DESC LIMIT 6`, route.chat, task)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line routingLine
		if err := rows.Scan(&line.Speaker, &line.Text); err != nil {
			return err
		}
		route.Recent = append(route.Recent, line)
	}
	slices.Reverse(route.Recent)
	return rows.Err()
}

func addressedGroupBud(message string, buds []AgentRef) string {
	message = strings.TrimSpace(message)
	prefix, _, delimited := strings.Cut(message, ",")
	if !delimited {
		prefix, _, delimited = strings.Cut(message, ":")
	}
	if !delimited {
		return ""
	}
	name := strings.TrimSpace(strings.TrimPrefix(prefix, "@"))
	var matched string
	for _, bud := range buds {
		if strings.EqualFold(bud.Name, name) {
			if matched != "" {
				return ""
			}
			matched = bud.ID
		}
	}
	return matched
}

func (w *Worker) routeGroupTurn(ctx context.Context, task Task) (Task, error) {
	route, err := w.store.groupRouting(ctx, task.ID)
	if err != nil || route == nil {
		return task, err
	}
	id := addressedGroupBud(route.Message, route.Buds)
	if id == "" && w.ChooseGroupBud != nil && route.Message != "" {
		if err := w.store.routingHistory(ctx, route, task.ID); err != nil {
			return task, err
		}
		decision, cancel := context.WithTimeout(ctx, 9*time.Second)
		id = w.ChooseGroupBud(decision, *route)
		cancel()
	}
	if ctx.Err() != nil {
		return task, ctx.Err()
	}
	if id == "" || !slices.ContainsFunc(route.Buds, func(b AgentRef) bool { return b.ID == id }) {
		id = route.Lead
	}
	if id == route.original {
		return task, nil
	}
	return w.store.assignGroupTurn(ctx, task, route.chat, id)
}

func (s *Store) assignGroupTurn(ctx context.Context, task Task, chat, id string) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	var status Status
	if err := tx.QueryRowContext(ctx, "SELECT status FROM tasks WHERE id=?", task.ID).Scan(&status); err != nil {
		return task, err
	}
	if status != Running {
		return task, ErrInactive
	}
	if err := requireOpenConversation(ctx, tx, chat); err != nil {
		return task, err
	}
	agent, err := conversationParticipant(ctx, tx, chat, id)
	var invalid *InputError
	if errors.Is(err, ErrNotFound) || errors.As(err, &invalid) || agent.WorkspaceNeedsReview {
		return task, nil // The original lead remains the safe fallback.
	}
	if err != nil {
		return task, err
	}
	for _, update := range []struct {
		query string
		args  []any
	}{
		{"UPDATE tasks SET repo=? WHERE id=?", []any{agent.Workspace, task.ID}},
		{"UPDATE chat_turns SET agent_name=?,instructions=?,allow_edits=? WHERE task_id=?", []any{agent.Name, agent.Instructions, agent.AllowEdits, task.ID}},
		{"UPDATE turn_details SET agent_id=?,agent_role=? WHERE task_id=?", []any{agent.ID, agent.Role, task.ID}},
		{"UPDATE turn_models SET model=? WHERE task_id=?", []any{agent.Model, task.ID}},
	} {
		if _, err := tx.ExecContext(ctx, update.query, update.args...); err != nil {
			return task, err
		}
	}
	if err := tx.Commit(); err != nil {
		return task, err
	}
	task.Repository = agent.Workspace
	return task, nil
}

func (r *CodexRuntime) ChooseGroupBud(ctx context.Context, route GroupRouting) string {
	if os.Getenv("BUDDY_DECISIONS_ROUTING") == "1" {
		if key, _, err := r.Key.Read(); err == nil && key != "" {
			if selected := decideGroupBud(ctx, openAIToolClient, key, route); selected != "" {
				return selected
			}
		}
	}
	if r.lookupErr != nil {
		return ""
	}
	dir, err := os.MkdirTemp("", "buddy-route-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	data, err := json.Marshal(route)
	if err != nil {
		return ""
	}
	turn := Turn{routing: true, Agent: Agent{Model: route.model}}
	emit := func(string, string) error { return nil }
	prompt := groupRoutingInstructions + "\n\n" + string(data)
	limits := slices.Concat(r.protected, r.readOnly, []string{r.migrationDir, r.snapshotDir, r.launcherDir})
	answer, err := r.runPrompt(ctx, dir, turn, limits, nil, nil, "", emit, prompt,
		func(run modelPass, _ []string) (string, error) { return run(prompt) })
	var result struct {
		Agent string `json:"agent_id"`
	}
	if err != nil || json.Unmarshal([]byte(answer), &result) != nil {
		return ""
	}
	return result.Agent
}

type PeerQuestion struct {
	AgentID  string `json:"agent_id"`
	Question string `json:"question"`
}

func enqueuePeerReply(ctx context.Context, tx *sql.Tx, task string, peer PeerQuestion) (string, error) {
	peer.Question = strings.TrimSpace(peer.Question)
	if peer.AgentID == "" || peer.Question == "" || len(peer.Question) > 1000 {
		return "I need a specific question and a Bud in this group to ask.", nil
	}
	var chat, agent, origin string
	if err := tx.QueryRowContext(ctx, `SELECT ct.conversation_id,td.agent_id,td.origin FROM turn_details td
		JOIN chat_turns ct ON ct.task_id=td.task_id WHERE td.task_id=?`, task).Scan(&chat, &agent, &origin); err != nil {
		return "", err
	}
	var member bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM group_members WHERE conversation_id=? AND agent_id=?)",
		chat, peer.AgentID).Scan(&member); err != nil {
		return "", err
	}
	if !member || peer.AgentID == agent || origin != "chat" {
		return "I can ask another Bud who’s in this group.", nil
	}
	if err := checkQueue(ctx, tx, false); err != nil {
		if errors.Is(err, ErrQueueFull) {
			return "I couldn’t bring in another Bud because the chat queue is full.", nil
		}
		return "", err
	}
	bud, err := conversationParticipant(ctx, tx, chat, peer.AgentID)
	var invalid *InputError
	if errors.As(err, &invalid) || bud.WorkspaceNeedsReview {
		return "That Bud isn’t available for a follow-up right now.", nil
	}
	if err != nil {
		return "", err
	}
	bud.Workspace, bud.AllowEdits = "", false
	_, err = enqueueTurn(ctx, tx, AgentTurn, "\x00peer:"+rand.Text(), chat, bud,
		workRequest{origin: "peer_reply", brief: peer.Question, parent: task})
	return "I’ll ask " + escapeTitle(bud.Name) + " to weigh in.", err
}
