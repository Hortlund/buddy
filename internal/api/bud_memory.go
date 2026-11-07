package api

import (
	"context"
	"net/http"
	"net/url"

	"buddy/internal/tasks"
)

type soulChange struct {
	Soul     string `json:"soul"`
	Revision string `json:"revision"`
}

type learningChange struct {
	Enabled *bool `json:"enabled"`
}

func budMemoryRoutes(mux *http.ServeMux, store *tasks.Store) {
	mux.HandleFunc("GET /v1/agents/{id}/documents", func(w http.ResponseWriter, r *http.Request) {
		docs, err := store.BudDocuments(r.Context(), r.PathValue("id"))
		result(w, http.StatusOK, docs, err)
	})
	mux.HandleFunc("PUT /v1/agents/{id}/documents/soul", func(w http.ResponseWriter, r *http.Request) {
		var input soulChange
		if err := decode(w, r, &input, 50<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		result(w, http.StatusOK, nil, store.SaveSoul(r.Context(), r.PathValue("id"), input.Soul, input.Revision))
	})
	mux.HandleFunc("PATCH /v1/agents/{id}/documents/memory", func(w http.ResponseWriter, r *http.Request) {
		var input learningChange
		if err := decode(w, r, &input, 1024); err != nil || input.Enabled == nil {
			result(w, 0, nil, &tasks.InputError{Message: "Choose whether the Bud should learn automatically."})
			return
		}
		result(w, http.StatusOK, nil, store.SetBudLearning(r.Context(), r.PathValue("id"), *input.Enabled))
	})
	mux.HandleFunc("DELETE /v1/agents/{id}/documents/memory", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.ClearLearnedMemory(r.Context(), r.PathValue("id")))
	})
}

func (c *Client) BudDocuments(ctx context.Context, bud string) (tasks.BudDocuments, error) {
	var out tasks.BudDocuments
	err := c.call(ctx, http.MethodGet, "/v1/agents/"+url.PathEscape(bud)+"/documents", "", nil, &out)
	return out, err
}

func (c *Client) SaveSoul(ctx context.Context, bud, soul, revision string) error {
	return c.call(ctx, http.MethodPut, "/v1/agents/"+url.PathEscape(bud)+"/documents/soul", "", soulChange{soul, revision}, nil)
}

func (c *Client) SetBudLearning(ctx context.Context, bud string, enabled bool) error {
	return c.call(ctx, http.MethodPatch, "/v1/agents/"+url.PathEscape(bud)+"/documents/memory", "", learningChange{&enabled}, nil)
}

func (c *Client) ClearLearnedMemory(ctx context.Context, bud string) error {
	return c.call(ctx, http.MethodDelete, "/v1/agents/"+url.PathEscape(bud)+"/documents/memory", "", nil, nil)
}
