package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

const codexKeyPath = "/v1/runtime/codex/key"
const codexModelsPath = "/v1/runtime/codex/models"

type CodexAuth struct {
	Source string `json:"source"`
	State  string `json:"state"`
}

func codexRoutes(mux *http.ServeMux, codex *tasks.CodexRuntime) {
	status := func(w http.ResponseWriter, r *http.Request) {
		_, source, err := codex.Key.Read()
		if err != nil {
			write(w, http.StatusInternalServerError, errorBody{"Couldn't read Buddy's Codex credential settings."})
			return
		}
		write(w, http.StatusOK, CodexAuth{Source: source, State: codex.RuntimeStatus(r.Context(), source)})
	}
	mux.HandleFunc("GET "+codexKeyPath, status)
	mux.HandleFunc("PUT "+codexKeyPath, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Key string `json:"key"`
		}
		if err := decode(w, r, &input, 4<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		key := strings.TrimSpace(input.Key)
		if !localconfig.ValidCodexKey(key) {
			write(w, http.StatusBadRequest, errorBody{"Enter a valid OpenAI API key."})
			return
		}
		if err := codex.Key.Save(key); err != nil {
			write(w, http.StatusInternalServerError, errorBody{"Couldn't save the API key."})
			return
		}
		status(w, r)
	})
	mux.HandleFunc("DELETE "+codexKeyPath, func(w http.ResponseWriter, r *http.Request) {
		if err := codex.Key.Remove(); err != nil {
			write(w, http.StatusInternalServerError, errorBody{"Couldn't remove the API key."})
			return
		}
		status(w, r)
	})
	mux.HandleFunc("GET "+codexModelsPath, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		models, err := codex.Models(ctx)
		if err != nil {
			write(w, http.StatusServiceUnavailable, errorBody{"Couldn't load the Codex model list."})
			return
		}
		write(w, http.StatusOK, models)
	})
}

func (c *Client) CodexModels(ctx context.Context) ([]tasks.Model, error) {
	var out []tasks.Model
	err := c.call(ctx, http.MethodGet, codexModelsPath, "", nil, &out)
	return out, err
}

func (c *Client) CodexAuth(ctx context.Context) (CodexAuth, error) {
	var out CodexAuth
	err := c.call(ctx, http.MethodGet, codexKeyPath, "", nil, &out)
	return out, err
}

func (c *Client) SaveCodexKey(ctx context.Context, key string) (CodexAuth, error) {
	var out CodexAuth
	err := c.call(ctx, http.MethodPut, codexKeyPath, "", struct {
		Key string `json:"key"`
	}{key}, &out)
	return out, err
}

func (c *Client) RemoveCodexKey(ctx context.Context) (CodexAuth, error) {
	var out CodexAuth
	err := c.call(ctx, http.MethodDelete, codexKeyPath, "", nil, &out)
	return out, err
}
