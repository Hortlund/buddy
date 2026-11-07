package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"buddy/internal/automations"
	"buddy/internal/tasks"
)

const StandingProtocol = 29
const StandingRecoveryProtocol = 30

func standingRoutes(mux *http.ServeMux, store *tasks.Store, service *automations.Service) {
	mux.HandleFunc("GET /v1/watches", func(w http.ResponseWriter, r *http.Request) {
		watches, err := store.Watches(r.Context())
		result(w, http.StatusOK, watches, err)
	})
	mux.HandleFunc("POST /v1/conversations/{id}/watch-drafts/{task}/activate", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := providerRequest(w, r)
		defer cancel()
		var prepare func(context.Context, *tasks.Watch) error
		if service != nil {
			prepare = func(ctx context.Context, watch *tasks.Watch) error {
				if err := service.ValidateWatchSource(ctx, *watch); err != nil {
					return err
				}
				return service.PrepareWatchStart(ctx, watch)
			}
		}
		err := store.ActivateChatWatchDraft(ctx, r.PathValue("id"), r.PathValue("task"), prepare)
		result(w, http.StatusOK, nil, err)
	})
	mux.HandleFunc("POST /v1/watches", func(w http.ResponseWriter, r *http.Request) {
		var draft tasks.Watch
		if err := decode(w, r, &draft, 16<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		ctx := r.Context()
		if service != nil && draft.ConnectionID != "" {
			var cancel context.CancelFunc
			ctx, cancel = providerRequest(w, r)
			defer cancel()
			if err := service.ValidateWatchSource(ctx, draft); err != nil {
				result(w, 0, nil, err)
				return
			}
			if err := service.PrepareWatchStart(ctx, &draft); err != nil {
				result(w, 0, nil, err)
				return
			}
		}
		watch, err := store.CreateWatch(ctx, draft)
		result(w, http.StatusCreated, watch, err)
	})
	mux.HandleFunc("PATCH /v1/watches/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decode(w, r, &input, 1024); err != nil || input.Enabled == nil {
			result(w, 0, nil, &tasks.InputError{Message: "Choose whether the watch is enabled."})
			return
		}
		id, ctx := r.PathValue("id"), r.Context()
		var watch tasks.Watch
		if *input.Enabled && service != nil {
			var err error
			watch, err = store.Watch(ctx, id)
			if err != nil {
				result(w, 0, nil, err)
				return
			}
			if !watch.Enabled && watch.Source == tasks.GmailLabel && watch.ConnectionID != "" {
				var cancel context.CancelFunc
				ctx, cancel = providerRequest(w, r)
				defer cancel()
				if err := service.PrepareWatchStart(ctx, &watch); err != nil {
					result(w, 0, nil, err)
					return
				}
			}
		}
		result(w, http.StatusOK, nil, store.SetWatchEnabledWithHistory(ctx, id, *input.Enabled, watch.GmailHistory))
	})
	mux.HandleFunc("PUT /v1/watches/{id}", func(w http.ResponseWriter, r *http.Request) {
		var draft tasks.Watch
		if err := decode(w, r, &draft, 16<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		watch, err := store.UpdateWatch(r.Context(), r.PathValue("id"), draft)
		result(w, http.StatusOK, watch, err)
	})
	mux.HandleFunc("DELETE /v1/watches/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.ArchiveWatch(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("GET /v1/inbox", func(w http.ResponseWriter, r *http.Request) {
		items, err := store.StandingInbox(r.Context())
		result(w, http.StatusOK, items, err)
	})
	mux.HandleFunc("GET /v1/inbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		item, err := store.StandingInboxItem(r.Context(), r.PathValue("id"))
		result(w, http.StatusOK, item, err)
	})
	mux.HandleFunc("PATCH /v1/inbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input inboxDecision
		if err := decode(w, r, &input, 1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		result(w, http.StatusOK, nil, store.UpdateInboxItem(r.Context(), r.PathValue("id"), input.Decision))
	})
	mux.HandleFunc("GET /v1/agents/{id}/memories", func(w http.ResponseWriter, r *http.Request) {
		notes, err := store.StandingMemories(r.Context(), r.PathValue("id"))
		result(w, http.StatusOK, notes, err)
	})
	mux.HandleFunc("PUT /v1/agents/{id}/memories", func(w http.ResponseWriter, r *http.Request) {
		var note tasks.MemoryNote
		if err := decode(w, r, &note, 8<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		note.AgentID = r.PathValue("id")
		saved, err := store.SaveStandingMemory(r.Context(), note)
		result(w, http.StatusOK, saved, err)
	})
	mux.HandleFunc("DELETE /v1/memories/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.DeleteStandingMemory(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("GET /v1/autonomy/health", func(w http.ResponseWriter, r *http.Request) {
		health, err := store.StandingHealth(r.Context())
		health.GitHub = "unconfigured"
		if service != nil {
			health.GitHub = service.GitHubDelivery()
		}
		result(w, http.StatusOK, health, err)
	})
	mux.HandleFunc("POST /v1/autonomy/notifications/retry", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.RetryStandingNotifications(r.Context()))
	})
}

type inboxDecision struct {
	Decision string `json:"decision"`
}

func (c *Client) Watches(ctx context.Context) ([]tasks.Watch, error) {
	var watches []tasks.Watch
	err := c.call(ctx, http.MethodGet, "/v1/watches", "", nil, &watches)
	return watches, err
}

func (c *Client) ActivateChatWatchDraft(ctx context.Context, chat, task string) error {
	return c.callWithTimeout(ctx, connectionTimeout+2*time.Second, http.MethodPost, "/v1/conversations/"+url.PathEscape(chat)+"/watch-drafts/"+url.PathEscape(task)+"/activate", "", nil, nil)
}

func (c *Client) CreateWatch(ctx context.Context, watch tasks.Watch) error {
	if watch.ID != "" {
		return c.call(ctx, http.MethodPut, "/v1/watches/"+url.PathEscape(watch.ID), "", watch, nil)
	}
	if watch.ConnectionID != "" {
		return c.callWithTimeout(ctx, connectionTimeout+2*time.Second, http.MethodPost, "/v1/watches", "", watch, nil)
	}
	return c.call(ctx, http.MethodPost, "/v1/watches", "", watch, nil)
}

func (c *Client) SetWatchEnabled(ctx context.Context, id string, enabled bool) error {
	return c.callWithTimeout(ctx, connectionTimeout+2*time.Second, http.MethodPatch, "/v1/watches/"+url.PathEscape(id), "",
		struct {
			Enabled bool `json:"enabled"`
		}{enabled}, nil)
}

func (c *Client) ArchiveWatch(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/watches/"+url.PathEscape(id), "", nil, nil)
}

func (c *Client) Inbox(ctx context.Context) ([]tasks.InboxItem, error) {
	var inbox []tasks.InboxItem
	err := c.call(ctx, http.MethodGet, "/v1/inbox", "", nil, &inbox)
	return inbox, err
}

func (c *Client) DecideInbox(ctx context.Context, id, decision string) error {
	return c.call(ctx, http.MethodPatch, "/v1/inbox/"+url.PathEscape(id), "", inboxDecision{decision}, nil)
}

func (c *Client) Memories(ctx context.Context, bud string) ([]tasks.MemoryNote, error) {
	var notes []tasks.MemoryNote
	err := c.call(ctx, http.MethodGet, "/v1/agents/"+url.PathEscape(bud)+"/memories", "", nil, &notes)
	return notes, err
}

func (c *Client) SaveMemory(ctx context.Context, note tasks.MemoryNote) error {
	return c.call(ctx, http.MethodPut, "/v1/agents/"+url.PathEscape(note.AgentID)+"/memories", "", note, nil)
}

func (c *Client) DeleteMemory(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/memories/"+url.PathEscape(id), "", nil, nil)
}

func (c *Client) StandingHealth(ctx context.Context) (tasks.StandingHealth, error) {
	var health tasks.StandingHealth
	err := c.call(ctx, http.MethodGet, "/v1/autonomy/health", "", nil, &health)
	return health, err
}

func (c *Client) RetryStandingNotifications(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/autonomy/notifications/retry", "", nil, nil)
}
