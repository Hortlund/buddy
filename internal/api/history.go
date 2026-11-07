package api

import (
	"context"
	"net/http"
	"net/url"
	"sync/atomic"

	"buddy/internal/tasks"
)

const ConversationHistoryProtocol = 49

type archiveChange struct {
	Archived *bool `json:"archived"`
}

func historyRoutes(mux *http.ServeMux, store *tasks.Store, voiceInUse *atomic.Bool) {
	change := func(apply func(*http.Request, bool) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var input archiveChange
			if err := decode(w, r, &input, 1024); err != nil || input.Archived == nil {
				result(w, 0, nil, &tasks.InputError{Message: "Choose whether to archive or restore."})
				return
			}
			if !voiceInUse.CompareAndSwap(false, true) {
				write(w, http.StatusConflict, errorBody{"End the active call before changing chat history."})
				return
			}
			defer voiceInUse.Store(false)
			result(w, http.StatusOK, nil, apply(r, *input.Archived))
		}
	}
	mux.HandleFunc("PATCH /v1/conversations/{id}/archive", change(func(r *http.Request, archived bool) error {
		return store.SetGroupArchived(r.Context(), r.PathValue("id"), archived)
	}))
	mux.HandleFunc("GET /v1/conversations/{id}/calls", func(w http.ResponseWriter, r *http.Request) {
		calls, err := store.Calls(r.Context(), r.PathValue("id"))
		result(w, http.StatusOK, calls, err)
	})
	mux.HandleFunc("PATCH /v1/conversations/{id}/calls/{call}", change(func(r *http.Request, archived bool) error {
		return store.SetCallArchived(r.Context(), r.PathValue("id"), r.PathValue("call"), archived)
	}))
}

func (c *Client) SetGroupArchived(ctx context.Context, chat string, archived bool) error {
	return c.call(ctx, http.MethodPatch, "/v1/conversations/"+url.PathEscape(chat)+"/archive", "", archiveChange{&archived}, nil)
}

func (c *Client) Calls(ctx context.Context, chat string) ([]tasks.CallHistory, error) {
	var out []tasks.CallHistory
	err := c.call(ctx, http.MethodGet, "/v1/conversations/"+url.PathEscape(chat)+"/calls", "", nil, &out)
	return out, err
}

func (c *Client) SetCallArchived(ctx context.Context, chat, call string, archived bool) error {
	path := "/v1/conversations/" + url.PathEscape(chat) + "/calls/" + url.PathEscape(call)
	return c.call(ctx, http.MethodPatch, path, "", archiveChange{&archived}, nil)
}
