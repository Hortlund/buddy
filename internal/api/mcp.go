package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"buddy/internal/tasks"
)

const mcpPath = "/v1/runtime/codex/mcp"

type mcpEnabled struct {
	Enabled bool `json:"enabled"`
}

func mcpRoutes(mux *http.ServeMux, store *tasks.Store, codex *tasks.CodexRuntime) {
	mux.HandleFunc("GET "+mcpPath, func(w http.ResponseWriter, r *http.Request) {
		servers, err := store.MCPServers(r.Context())
		result(w, http.StatusOK, servers, err)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var input tasks.MCPServerSetup
		if err := decode(w, r, &input, 96<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		server, err := store.SaveMCPServer(r.Context(), r.PathValue("id"), input)
		result(w, http.StatusOK, server, err)
	}
	mux.HandleFunc("POST "+mcpPath, save)
	mux.HandleFunc("PUT "+mcpPath+"/{id}", save)
	mux.HandleFunc("PUT "+mcpPath+"/{id}/enabled", func(w http.ResponseWriter, r *http.Request) {
		var input mcpEnabled
		if err := decode(w, r, &input, 512); err != nil {
			result(w, 0, nil, err)
			return
		}
		server, err := store.SetMCPServerEnabled(r.Context(), r.PathValue("id"), input.Enabled)
		result(w, http.StatusOK, server, err)
	})
	mux.HandleFunc("DELETE "+mcpPath+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := store.DeleteMCPServer(r.Context(), r.PathValue("id"))
		result(w, http.StatusNoContent, nil, err)
	})
	mux.HandleFunc("POST "+mcpPath+"/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		server, err := store.MCPServerConfig(ctx, r.PathValue("id"))
		if err != nil {
			result(w, 0, nil, err)
			return
		}
		check, err := codex.TestMCPServer(ctx, server)
		result(w, http.StatusOK, check, err)
	})
}

func (c *Client) MCPServers(ctx context.Context) ([]tasks.MCPServer, error) {
	var out []tasks.MCPServer
	err := c.call(ctx, http.MethodGet, mcpPath, "", nil, &out)
	return out, err
}

func (c *Client) SaveMCPServer(ctx context.Context, id string, input tasks.MCPServerSetup) (tasks.MCPServer, error) {
	method, path := http.MethodPost, mcpPath
	if id != "" {
		method, path = http.MethodPut, mcpPath+"/"+url.PathEscape(id)
	}
	var out tasks.MCPServer
	err := c.call(ctx, method, path, "", input, &out)
	return out, err
}

func (c *Client) SetMCPServerEnabled(ctx context.Context, id string, enabled bool) (tasks.MCPServer, error) {
	var out tasks.MCPServer
	err := c.call(ctx, http.MethodPut, mcpPath+"/"+url.PathEscape(id)+"/enabled", "", mcpEnabled{enabled}, &out)
	return out, err
}

func (c *Client) DeleteMCPServer(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, mcpPath+"/"+url.PathEscape(id), "", nil, nil)
}

func (c *Client) TestMCPServer(ctx context.Context, id string) (tasks.MCPTestResult, error) {
	var out tasks.MCPTestResult
	err := c.callWithTimeout(ctx, 14*time.Second, http.MethodPost, mcpPath+"/"+url.PathEscape(id)+"/test", "", nil, &out)
	return out, err
}
