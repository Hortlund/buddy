package api

import (
	"context"
	"net/http"
	"net/url"
	"sync/atomic"

	"buddy/internal/tasks"
)

const BudSettingsProtocol = 19
const WorkspaceFilesProtocol = 28
const RemoveBudProtocol = 33
const ArchivedWorkProtocol = 34

type leadershipChange struct {
	AgentID string `json:"agent_id"`
}

func workspaceRoutes(mux *http.ServeMux, store *tasks.Store, voiceInUse *atomic.Bool) {
	mux.HandleFunc("GET /v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		directory, err := store.Directory(r.Context())
		result(w, http.StatusOK, directory, err)
	})
	mux.HandleFunc("PUT /v1/workspace/primary", func(w http.ResponseWriter, r *http.Request) {
		var input leadershipChange
		if err := decode(w, r, &input, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		result(w, http.StatusOK, nil, store.SetPrimaryAgent(r.Context(), input.AgentID))
	})
	mux.HandleFunc("PUT /v1/conversations/{id}/lead", func(w http.ResponseWriter, r *http.Request) {
		var input leadershipChange
		if err := decode(w, r, &input, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		result(w, http.StatusOK, nil, store.SetGroupLead(r.Context(), r.PathValue("id"), input.AgentID))
	})
	mux.HandleFunc("POST /v1/agents", func(w http.ResponseWriter, r *http.Request) {
		var input tasks.Agent
		if err := decode(w, r, &input, 256<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		agent, err := store.CreateAgent(r.Context(), input)
		result(w, http.StatusCreated, agent, err)
	})
	mux.HandleFunc("DELETE /v1/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !voiceInUse.CompareAndSwap(false, true) {
			write(w, http.StatusConflict, errorBody{"End the call before removing a Bud."})
			return
		}
		defer voiceInUse.Store(false)
		result(w, http.StatusOK, nil, store.RemoveAgent(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("GET /v1/agents/{id}/workspace", func(w http.ResponseWriter, r *http.Request) {
		workspace, err := store.Workspace(r.Context(), r.PathValue("id"))
		result(w, http.StatusOK, workspace, err)
	})
	mux.HandleFunc("POST /v1/conversations", func(w http.ResponseWriter, r *http.Request) {
		var input tasks.GroupRequest
		if err := decode(w, r, &input, 8<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		chat, err := store.CreateGroup(r.Context(), input)
		result(w, http.StatusCreated, chat, err)
	})
	mux.HandleFunc("POST /v1/conversations/{id}/read", func(w http.ResponseWriter, r *http.Request) {
		var input readReceipt
		if err := decode(w, r, &input, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		err := store.MarkResultsRead(r.Context(), r.PathValue("id"), input.Through)
		result(w, http.StatusOK, nil, err)
	})
	mux.HandleFunc("POST /v1/conversations/{id}/routines", func(w http.ResponseWriter, r *http.Request) {
		var input tasks.Routine
		if err := decode(w, r, &input, 80<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		routine, err := store.AddRoutine(r.Context(), r.PathValue("id"), input)
		result(w, http.StatusCreated, routine, err)
	})
	mux.HandleFunc("PATCH /v1/conversations/{id}/routines/{routine}", func(w http.ResponseWriter, r *http.Request) {
		var input routineChange
		if err := decode(w, r, &input, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		if input.Enabled == nil {
			result(w, 0, nil, &tasks.InputError{Message: "Choose whether the routine should be enabled."})
			return
		}
		err := store.UpdateRoutine(r.Context(), r.PathValue("id"), r.PathValue("routine"), input.Enabled)
		result(w, http.StatusOK, nil, err)
	})
	mux.HandleFunc("DELETE /v1/conversations/{id}/routines/{routine}", func(w http.ResponseWriter, r *http.Request) {
		err := store.UpdateRoutine(r.Context(), r.PathValue("id"), r.PathValue("routine"), nil)
		result(w, http.StatusOK, nil, err)
	})
}

type routineChange struct {
	Enabled *bool `json:"enabled"`
}

func (c *Client) Directory(ctx context.Context) (tasks.Directory, error) {
	var out tasks.Directory
	err := c.call(ctx, http.MethodGet, "/v1/workspace", "", nil, &out)
	return out, err
}

func (c *Client) RemoveAgent(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/agents/"+url.PathEscape(id), "", nil, nil)
}

func (c *Client) SetPrimaryAgent(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPut, "/v1/workspace/primary", "", leadershipChange{AgentID: id}, nil)
}

func (c *Client) SetGroupLead(ctx context.Context, chat, id string) error {
	return c.call(ctx, http.MethodPut, "/v1/conversations/"+url.PathEscape(chat)+"/lead", "", leadershipChange{AgentID: id}, nil)
}

func (c *Client) Workspace(ctx context.Context, id string) (tasks.Workspace, error) {
	var out tasks.Workspace
	err := c.call(ctx, http.MethodGet, "/v1/agents/"+url.PathEscape(id)+"/workspace", "", nil, &out)
	return out, err
}

func workspaceFileRoutes(mux *http.ServeMux, store *tasks.Store, runtime *tasks.CodexRuntime) {
	mux.HandleFunc("GET /v1/agents/{id}/workspace/item", func(w http.ResponseWriter, r *http.Request) {
		allowAttachmentTransfer(w, false)
		query := r.URL.Query()
		item, err := runtime.WorkspaceItem(r.Context(), store, r.PathValue("id"), query.Get("workspace"), query.Get("path"), query.Get("task"))
		result(w, http.StatusOK, item, err)
	})
	mux.HandleFunc("PUT /v1/agents/{id}/workspace/item", func(w http.ResponseWriter, r *http.Request) {
		allowAttachmentTransfer(w, true)
		var input struct {
			Revision string  `json:"revision"`
			Text     *string `json:"text"`
		}
		if err := decode(w, r, &input, 6*tasks.MaxWorkspaceTextBytes+1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		if input.Text == nil {
			result(w, 0, nil, &tasks.InputError{Message: "Include the file text, even when saving an empty file."})
			return
		}
		query := r.URL.Query()
		edit := tasks.WorkspaceFileEdit{Revision: input.Revision, Text: *input.Text}
		revision, err := runtime.SaveWorkspaceFile(r.Context(), store, r.PathValue("id"), query.Get("workspace"), query.Get("path"), edit)
		result(w, http.StatusOK, fileRevision{Revision: revision}, err)
	})
}

type fileRevision struct {
	Revision string `json:"revision"`
}

func workspaceItemURL(id, workspace, relative string) string {
	query := url.Values{"path": {relative}, "workspace": {workspace}}
	return "/v1/agents/" + url.PathEscape(id) + "/workspace/item?" + query.Encode()
}

func (c *Client) WorkspaceItem(ctx context.Context, id, workspace, relative string, source ...string) (tasks.WorkspaceItem, error) {
	var out tasks.WorkspaceItem
	path := workspaceItemURL(id, workspace, relative)
	if len(source) != 0 && source[0] != "" {
		path += "&task=" + url.QueryEscape(source[0])
	}
	err := c.callWithTimeout(ctx, attachmentTransferTimeout, http.MethodGet, path, "", nil, &out)
	if err == nil && len(source) != 0 && source[0] != "" && out.SourceTask != source[0] {
		err = &tasks.InputError{Message: "Update the Buddy server to open task-bound file references safely."}
	}
	return out, err
}

func (c *Client) SaveWorkspaceFile(ctx context.Context, id, workspace, relative string, edit tasks.WorkspaceFileEdit) (string, error) {
	var out fileRevision
	err := c.callWithTimeout(ctx, attachmentTransferTimeout, http.MethodPut, workspaceItemURL(id, workspace, relative), "", edit, &out)
	return out.Revision, err
}

func (c *Client) CreateAgent(ctx context.Context, agent tasks.Agent) (tasks.Agent, error) {
	var out tasks.Agent
	err := c.call(ctx, http.MethodPost, "/v1/agents", "", agent, &out)
	return out, err
}

func (c *Client) CreateGroup(ctx context.Context, input tasks.GroupRequest) (tasks.ChatRef, error) {
	var out tasks.ChatRef
	err := c.call(ctx, http.MethodPost, "/v1/conversations", "", input, &out)
	return out, err
}

func (c *Client) AddRoutine(ctx context.Context, chat string, input tasks.Routine) error {
	var out tasks.Routine
	return c.call(ctx, http.MethodPost, "/v1/conversations/"+url.PathEscape(chat)+"/routines", "", input, &out)
}

func (c *Client) UpdateRoutine(ctx context.Context, chat, id string, enabled *bool) error {
	path := "/v1/conversations/" + url.PathEscape(chat) + "/routines/" + url.PathEscape(id)
	if enabled == nil {
		return c.call(ctx, http.MethodDelete, path, "", nil, nil)
	}
	return c.call(ctx, http.MethodPatch, path, "", routineChange{Enabled: enabled}, nil)
}

type readReceipt struct {
	Through int64 `json:"through"`
}

func (c *Client) MarkResultsRead(ctx context.Context, chat string, through int64) error {
	return c.call(ctx, http.MethodPost, "/v1/conversations/"+url.PathEscape(chat)+"/read", "", readReceipt{Through: through}, nil)
}
