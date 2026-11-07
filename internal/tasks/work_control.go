package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const workControlSchema = `
CREATE TABLE IF NOT EXISTS work_control_tokens(task_id TEXT PRIMARY KEY,device_id TEXT NOT NULL,lease TEXT NOT NULL,token_hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS work_control_receipts(task_id TEXT NOT NULL,request_id TEXT NOT NULL,result TEXT NOT NULL,PRIMARY KEY(task_id,request_id));
`

var workControlTools = []map[string]any{
	{"name": "team_context", "description": "Read current team, lead, projects, hosts and work status. Refresh before assigning or duplicating work.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
	{"name": "inspect_work", "description": "Read an assignment's live progress and result in this conversation.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"job_id": map[string]any{"type": "string"}}, "required": []string{"job_id"}, "additionalProperties": false}},
	{"name": "post_update", "description": "Post a concise, factual progress update for this assignment.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}, "request_id": map[string]any{"type": "string"}}, "required": []string{"text", "request_id"}, "additionalProperties": false}},
	{"name": "delegate", "description": "Assign a focused subtask within this assignment to a Bud in this conversation. Use a stable request_id for retries. Set worker_role when useful, for example review or research. Keep scope within the user's goal. Do not wait synchronously; inspect work later.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"agent_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "brief": map[string]any{"type": "string"}, "worker_role": map[string]any{"type": "string"}, "read_only": map[string]any{"type": "boolean"}, "request_id": map[string]any{"type": "string"}}, "required": []string{"agent_id", "title", "brief", "read_only", "request_id"}, "additionalProperties": false}},
}

func (s *Store) AuthenticateWorkControl(ctx context.Context, job, token string) bool {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_control_tokens c JOIN device_runs dr ON dr.task_id=c.task_id
		JOIN tasks t ON t.id=c.task_id JOIN devices d ON d.id=c.device_id
		WHERE c.task_id=? AND c.token_hash=? AND dr.lease=c.lease AND dr.device_id=c.device_id AND dr.expires>?
		AND d.revoked=0 AND t.status='running')`, job, tokenHash(token), time.Now().UnixMicro()).Scan(&allowed)
	return err == nil && allowed
}

func (s *Store) controlWork(ctx context.Context, job, name string, raw json.RawMessage) (any, error) {
	var chat, owner string
	if err := s.db.QueryRowContext(ctx, `SELECT ct.conversation_id,td.agent_id FROM tasks t JOIN chat_turns ct ON ct.task_id=t.id
		JOIN turn_details td ON td.task_id=t.id WHERE t.id=? AND t.kind='background_work' AND t.status='running'`, job).Scan(&chat, &owner); err != nil {
		return nil, ErrInactive
	}
	switch name {
	case "team_context":
		team, err := s.TeamContext(ctx, chat, owner)
		if err != nil {
			return nil, err
		}
		projects, err := s.Projects(ctx)
		if err != nil {
			return nil, err
		}
		hosts, err := s.Hosts(ctx)
		return map[string]any{"team": team, "projects": projects, "hosts": hosts}, err
	case "inspect_work":
		var input struct {
			Job string `json:"job_id"`
		}
		if json.Unmarshal(raw, &input) != nil {
			return nil, &InputError{"Provide a job ID."}
		}
		return s.WorkDetails(ctx, chat, input.Job)
	}
	var input struct {
		Text     string `json:"text"`
		Request  string `json:"request_id"`
		Agent    string `json:"agent_id"`
		Title    string `json:"title"`
		Brief    string `json:"brief"`
		Role     string `json:"worker_role"`
		ReadOnly bool   `json:"read_only"`
	}
	if json.Unmarshal(raw, &input) != nil || !validTitle(input.Request, 120) {
		return nil, &InputError{"Provide a stable request_id."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRowContext(ctx, "SELECT result FROM work_control_receipts WHERE task_id=? AND request_id=?", job, input.Request).Scan(&prior)
	if err == nil {
		var out any
		err = json.Unmarshal([]byte(prior), &out)
		return out, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, "SELECT status='running' FROM tasks WHERE id=?", job).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, ErrInactive
	}
	var out any
	switch name {
	case "post_update":
		if !validTitle(input.Text, 1500) {
			return nil, &InputError{"Use a concise progress update."}
		}
		err = addEvent(ctx, tx, job, "work_update", input.Text)
		out = map[string]bool{"saved": true}
	case "delegate":
		var count, depth int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM turn_details WHERE parent_id=?", job).Scan(&count); err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent,depth) AS (
			SELECT task_id,parent_id,1 FROM turn_details WHERE task_id=? UNION ALL SELECT t.task_id,t.parent_id,a.depth+1
			FROM turn_details t JOIN ancestors a ON t.task_id=a.parent WHERE a.depth<4) SELECT max(depth) FROM ancestors`, job).Scan(&depth); err != nil {
			return nil, err
		}
		if count >= 8 || depth >= 4 {
			return nil, &InputError{"This assignment's delegation budget has been reached. Report the remaining work to the caller."}
		}
		plan, planErr := readExecutionPlan(ctx, tx, job)
		if planErr != nil {
			return nil, planErr
		}
		if input.Role != "" {
			plan.Role = input.Role
		}
		var repo string
		var edits bool
		if err = tx.QueryRowContext(ctx, "SELECT t.repo,c.allow_edits FROM tasks t JOIN chat_turns c ON c.task_id=t.id WHERE t.id=?", job).Scan(&repo, &edits); err != nil {
			return nil, err
		}
		out, err = enqueueAssignments(ctx, tx, assignmentSource{chat: chat, requester: owner, key: "worker:" + job + ":" + input.Request, parent: job, repo: repo, allowEdits: edits, readOnly: !edits}, []Handoff{{ExecutionPlan: plan, AgentID: input.Agent, Title: input.Title, Brief: input.Brief, ReadOnly: input.ReadOnly}})
	default:
		return nil, &InputError{"Unknown Buddy work tool."}
	}
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO work_control_receipts VALUES (?,?,?)", job, input.Request, string(data)); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Streamable HTTP MCP, bound to one running assignment. Its bearer is kept in
// the isolated runner config, outside the model's readable filesystem.
func (s *Store) WorkControlHandler(job string, authorize func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorize(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
				Protocol  string          `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&request) != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var value any
		var err error
		switch request.Method {
		case "initialize":
			value = map[string]any{"protocolVersion": request.Params.Protocol, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "buddy-work", "version": "1"}}
		case "ping":
			value = map[string]any{}
		case "tools/list":
			value = map[string]any{"tools": workControlTools}
		case "tools/call":
			value, err = s.controlWork(r.Context(), job, request.Params.Name, request.Params.Arguments)
			if err != nil {
				value = map[string]any{"content": []any{map[string]string{"type": "text", "text": err.Error()}}, "isError": true}
				err = nil
			} else {
				data, _ := json.Marshal(value)
				value = map[string]any{"content": []any{map[string]string{"type": "text", "text": string(data)}}}
			}
		default:
			err = &InputError{"Unknown MCP method."}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": value}
		if err != nil {
			delete(response, "result")
			response["error"] = map[string]any{"code": -32601, "message": err.Error()}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

func (r *CodexRuntime) localWorkControl(ctx context.Context, turn Turn) (*MCPServerConfig, func(), error) {

	if (r.MCPStore == nil && turn.ControlForward == nil) || turn.Title == "" || turn.WatchSignal != nil || turn.BuddyTeam.CurrentTaskID == "" {
		return nil, func() {}, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	token := rand.Text() + rand.Text()
	var handler http.Handler
	if turn.ControlForward != nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "Unauthorized", 401)
				return
			}
			if req.Method != http.MethodPost {
				w.WriteHeader(405)
				return
			}
			raw, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 32<<10))
			if err != nil {
				http.Error(w, "Invalid request", 400)
				return
			}
			response, err := turn.ControlForward(req.Context(), raw)
			if err != nil {
				http.Error(w, "The work control service is unavailable", 502)
				return
			}
			if len(response) == 0 {
				w.WriteHeader(202)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(response)
		})
	} else {
		handler = r.MCPStore.WorkControlHandler(turn.BuddyTeam.CurrentTaskID, func(req *http.Request) bool { return req.Header.Get("Authorization") == "Bearer "+token })
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	close := func() { server.Close() }
	return &MCPServerConfig{MCPServer: MCPServer{Name: "buddy-work", Transport: MCPHTTP, URL: "http://" + listener.Addr().String(), Enabled: true, Tools: []string{"team_context", "inspect_work", "post_update", "delegate"}, ApproveTools: true}, token: token}, close, nil
}

func bearer(req *http.Request) string {
	token, _ := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	return token
}
