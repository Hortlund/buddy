package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"buddy/internal/tasks"
)

const ExecutionProtocol = 58

func executionRoutes(mux *http.ServeMux, store *tasks.Store, runtime *tasks.CodexRuntime) {
	mux.HandleFunc("GET /v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		devices, err := store.Devices(r.Context())
		if err != nil {
			result(w, 0, nil, err)
			return
		}
		profile, err := store.HostProfile(r.Context(), "server")
		hosts := append([]tasks.Device{{ID: "server", Name: "Buddy server", Online: true, Seen: true, Profile: &profile}}, devices...)
		result(w, http.StatusOK, hosts, err)
	})
	mux.HandleFunc("GET /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		projects, err := store.Projects(r.Context())
		result(w, http.StatusOK, projects, err)
	})
	mux.HandleFunc("POST /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		var input tasks.Project
		if err := decode(w, r, &input, 8192); err != nil {
			result(w, 0, nil, err)
			return
		}
		project, err := store.SaveProject(r.Context(), input)
		result(w, http.StatusOK, project, err)
	})
	mux.HandleFunc("DELETE /v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.ArchiveProject(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("GET /v1/work/{job}/artifacts/{file}", func(w http.ResponseWriter, r *http.Request) {
		allowAttachmentTransfer(w, false)
		artifact, err := store.WorkArtifact(r.Context(), r.PathValue("job"), r.PathValue("file"))
		result(w, http.StatusOK, artifact, err)
	})
	mux.HandleFunc("GET /v1/work/{job}/frame", func(w http.ResponseWriter, r *http.Request) {
		job := r.PathValue("job")
		if _, err := store.ReferencedWorkDetails(r.Context(), job); err != nil {
			result(w, 0, nil, err)
			return
		}
		frame, err := store.StoredExecutionFrame(r.Context(), job)
		if err == nil && len(frame.Data) == 0 && runtime != nil {
			frame, err = runtime.ExecutionFrame(job)
		}
		if err == nil {
			frame.Paused, err = store.AutomationPaused(r.Context(), job)
		}
		w.Header().Set("Cache-Control", "no-store")
		result(w, http.StatusOK, frame, err)
	})
	mux.HandleFunc("POST /v1/work/{job}/control", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Paused bool `json:"paused"`
		}
		if err := decode(w, r, &input, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		err := store.SetAutomationPaused(r.Context(), r.PathValue("job"), input.Paused)
		if err == nil && runtime != nil {
			err = runtime.PauseAutomation(r.PathValue("job"), input.Paused)
		}
		result(w, http.StatusOK, nil, err)
	})
	mux.HandleFunc("GET /v1/execution/vms", func(w http.ResponseWriter, r *http.Request) {
		vms, err := tasks.LumeVMs(r.Context())
		result(w, http.StatusOK, vms, err)
	})
	mux.HandleFunc("POST /v1/execution/vms/{name}/{action}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, tasks.ChangeLumeVM(r.Context(), r.PathValue("name"), r.PathValue("action")))
	})
	if runtime == nil {
		return
	}
	mux.HandleFunc("GET /v1/execution", func(w http.ResponseWriter, r *http.Request) { write(w, http.StatusOK, runtime.ExecutionConfig()) })
	mux.HandleFunc("PUT /v1/execution", func(w http.ResponseWriter, r *http.Request) {
		var input tasks.ExecutionConfig
		if err := decode(w, r, &input, 64<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		err := runtime.PersistExecution(r.Context(), input)
		if err == nil {
			err = store.SaveHostProfile(r.Context(), "server", runtime.DiscoverHost(r.Context()))
		}
		result(w, http.StatusOK, runtime.ExecutionConfig(), err)
	})
	mux.HandleFunc("POST /v1/execution/automation/{provider}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(w, r, &input, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		provider := r.PathValue("provider")
		cfg := runtime.ExecutionConfig()
		cfg.Automation = slices.DeleteFunc(cfg.Automation, func(a tasks.AutomationSetup) bool { return a.Provider == provider })
		if input.Enabled {
			setup, err := runtime.TestAutomation(r.Context(), provider)
			if err != nil {
				result(w, 0, nil, err)
				return
			}
			cfg.Automation = append(cfg.Automation, setup)
		}
		err := runtime.PersistExecution(r.Context(), cfg)
		if err == nil {
			err = store.SaveHostProfile(r.Context(), "server", runtime.DiscoverHost(r.Context()))
		}
		result(w, http.StatusOK, runtime.ExecutionConfig(), err)
	})
}

func (c *Client) Hosts(ctx context.Context) ([]tasks.Device, error) {
	var out []tasks.Device
	err := c.call(ctx, http.MethodGet, "/v1/hosts", "", nil, &out)
	return out, err
}
func (c *Client) Projects(ctx context.Context) ([]tasks.Project, error) {
	var out []tasks.Project
	err := c.call(ctx, http.MethodGet, "/v1/projects", "", nil, &out)
	return out, err
}
func (c *Client) SaveProject(ctx context.Context, p tasks.Project) (tasks.Project, error) {
	var out tasks.Project
	err := c.call(ctx, http.MethodPost, "/v1/projects", "", p, &out)
	return out, err
}
func (c *Client) RemoveProject(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/projects/"+url.PathEscape(id), "", nil, nil)
}
func (c *Client) ExecutionConfig(ctx context.Context) (tasks.ExecutionConfig, error) {
	var out tasks.ExecutionConfig
	err := c.call(ctx, http.MethodGet, "/v1/execution", "", nil, &out)
	return out, err
}
func (c *Client) SaveExecutionConfig(ctx context.Context, cfg tasks.ExecutionConfig) error {
	return c.call(ctx, http.MethodPut, "/v1/execution", "", cfg, nil)
}
func (c *Client) EnableAutomation(ctx context.Context, provider string, enabled bool) error {
	return c.callWithTimeout(ctx, attachmentTransferTimeout, http.MethodPost, "/v1/execution/automation/"+url.PathEscape(provider), "", map[string]bool{"enabled": enabled}, nil)
}
func (c *Client) WorkArtifact(ctx context.Context, job, file string) (tasks.WorkArtifact, error) {
	var out tasks.WorkArtifact
	err := c.callWithTimeout(ctx, attachmentTransferTimeout, http.MethodGet, "/v1/work/"+url.PathEscape(job)+"/artifacts/"+url.PathEscape(file), "", nil, &out)
	return out, err
}

func (c *Client) ExecutionFrame(ctx context.Context, job string) (tasks.ExecutionFrame, error) {
	var out tasks.ExecutionFrame
	err := c.call(ctx, http.MethodGet, "/v1/work/"+url.PathEscape(job)+"/frame", "", nil, &out)
	return out, err
}
func (c *Client) PauseAutomation(ctx context.Context, job string, paused bool) error {
	return c.call(ctx, http.MethodPost, "/v1/work/"+url.PathEscape(job)+"/control", "", map[string]bool{"paused": paused}, nil)
}
func (c *Client) LumeVMs(ctx context.Context) ([]tasks.LumeVM, error) {
	var out []tasks.LumeVM
	err := c.call(ctx, http.MethodGet, "/v1/execution/vms", "", nil, &out)
	return out, err
}
func (c *Client) ChangeLumeVM(ctx context.Context, name, action string) error {
	return c.call(ctx, http.MethodPost, "/v1/execution/vms/"+url.PathEscape(name)+"/"+url.PathEscape(action), "", nil, nil)
}
