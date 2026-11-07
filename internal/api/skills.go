package api

import (
	"context"
	"net/http"
	"net/url"

	"buddy/internal/tasks"
)

const SkillsProtocol = 31
const SkillSelectionProtocol = 32

type skillImport struct {
	Markdown string `json:"markdown"`
}

func skillRoutes(mux *http.ServeMux, store *tasks.Store) {
	mux.HandleFunc("GET /v1/skills", func(w http.ResponseWriter, r *http.Request) {
		skills, err := store.Skills(r.Context())
		result(w, http.StatusOK, skills, err)
	})
	mux.HandleFunc("POST /v1/skills", func(w http.ResponseWriter, r *http.Request) {
		var input skillImport
		if err := decode(w, r, &input, 6*tasks.MaxSkillBytes+1024); err != nil {
			result(w, 0, nil, err)
			return
		}
		skill, err := store.InstallSkill(r.Context(), input.Markdown)
		result(w, http.StatusOK, skill, err)
	})
	mux.HandleFunc("DELETE /v1/skills/{name}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.RemoveSkill(r.Context(), r.PathValue("name")))
	})
	mux.HandleFunc("POST /v1/conversations/{id}/bud-proposals/{turn}", func(w http.ResponseWriter, r *http.Request) {
		bud, err := store.CreateProposedBud(r.Context(), r.PathValue("id"), r.PathValue("turn"))
		result(w, http.StatusOK, bud, err)
	})
}

func (c *Client) InstallSkill(ctx context.Context, markdown string) (tasks.SkillInfo, error) {
	var skill tasks.SkillInfo
	err := c.call(ctx, http.MethodPost, "/v1/skills", "", skillImport{Markdown: markdown}, &skill)
	return skill, err
}

func (c *Client) RemoveSkill(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodDelete, "/v1/skills/"+url.PathEscape(name), "", nil, nil)
}

func (c *Client) CreateProposedBud(ctx context.Context, chat, turn string) (tasks.Agent, error) {
	var bud tasks.Agent
	err := c.call(ctx, http.MethodPost, "/v1/conversations/"+url.PathEscape(chat)+"/bud-proposals/"+url.PathEscape(turn), "", nil, &bud)
	return bud, err
}
