package tasks

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const execToolTokenEnv = "BUDDY_EXEC_TOOL_BEARER"

type execChatBridge struct {
	config string
	token  string
}

// Keyring-backed exec uses the existing Codex home for authentication. Pass
// Buddy's tools as a per-process MCP override while ignoring the home config.
func serveExecChatTools(ctx context.Context, tools map[string]chatTool) (execChatBridge, func(), error) {
	if len(tools) == 0 {
		return execChatBridge{}, func() {}, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return execChatBridge{}, nil, err
	}
	bridge := execChatBridge{
		config: `mcp_servers={buddy_builtin={url=` + tomlString("http://"+listener.Addr().String()) +
			`,bearer_token_env_var="` + execToolTokenEnv + `",default_tools_approval_mode="approve"}}`,
		token: rand.Text() + rand.Text(),
	}
	byName := make(map[string]chatTool, len(tools))
	var catalog []execChatTool
	for _, tool := range tools {
		byName[tool.Name] = tool
		catalog = append(catalog, execChatTool{tool.Name, tool.Description, tool.InputSchema,
			map[string]bool{"readOnlyHint": tool.Name != "run_code"}})
	}
	slices.SortFunc(catalog, func(a, b execChatTool) int { return strings.Compare(a.Name, b.Name) })
	// A turn's tool state is shared with its completion pass.
	var calls sync.Mutex
	closed := false
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.BaseContext = func(net.Listener) context.Context { return ctx }
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if subtle.ConstantTimeCompare([]byte(bearer(req)), []byte(bridge.token)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
				Protocol  string          `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, req.Body, 512<<10)).Decode(&message) != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if len(message.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": message.ID}
		switch message.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": message.Params.Protocol,
				"capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "buddy_builtin", "version": "1"}}
		case "tools/list":
			response["result"] = map[string]any{"tools": catalog}
		case "tools/call":
			tool, found := byName[message.Params.Name]
			result := chatToolText{text: "This tool is unavailable for this turn."}
			if found {
				calls.Lock()
				if !closed && ctx.Err() == nil {
					args := message.Params.Arguments
					if len(args) == 0 {
						args = json.RawMessage(`{}`)
					}
					result = tool.call(args)
				}
				calls.Unlock()
			}
			response["result"] = map[string]any{"content": []map[string]string{{"type": "text", "text": result.text}}, "isError": !result.success}
		case "ping":
			response["result"] = map[string]any{}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "Unknown MCP method."}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
	go server.Serve(listener)
	return bridge, func() {
		_ = server.Close()
		calls.Lock()
		closed = true
		calls.Unlock()
	}, nil
}

type execChatTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations"`
}
