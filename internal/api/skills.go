package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"buddy/internal/tasks"
)

const SkillsProtocol = 31
const SkillSelectionProtocol = 32

type skillImport struct {
	Markdown string `json:"markdown"`
}

type budApprovalRequest struct {
	Approval tasks.BudApproval `json:"approval"`
}

const budApprovalRoute = "POST /v1/conversations/{id}/bud-proposals/{turn}/approval"

func skillRoutes(mux *http.ServeMux, store *tasks.Store, worker *tasks.Worker) {
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
	approve := func(w http.ResponseWriter, r *http.Request) {
		var input budApprovalRequest
		if r.Pattern == budApprovalRoute || r.ContentLength != 0 {
			if err := decode(w, r, &input, 1024); err != nil {
				result(w, 0, nil, err)
				return
			}
			if input.Approval == "" {
				result(w, 0, nil, &tasks.InputError{Message: "Choose how to create this Bud."})
				return
			}
		}
		bud, err := store.CreateProposedBud(r.Context(), r.PathValue("id"), r.PathValue("turn"), input.Approval)
		if err == nil && worker != nil && input.Approval == tasks.CreateBudWithWork {
			worker.WakeBackground()
		}
		result(w, http.StatusOK, bud, err)
	}
	mux.HandleFunc("POST /v1/conversations/{id}/bud-proposals/{turn}", approve)
	mux.HandleFunc(budApprovalRoute, approve)
}

func (c *Client) InstallSkill(ctx context.Context, markdown string) (tasks.SkillInfo, error) {
	var skill tasks.SkillInfo
	err := c.call(ctx, http.MethodPost, "/v1/skills", "", skillImport{Markdown: markdown}, &skill)
	return skill, err
}

func (c *Client) RemoveSkill(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodDelete, "/v1/skills/"+url.PathEscape(name), "", nil, nil)
}

func (c *Client) CreateProposedBud(ctx context.Context, chat, turn string, approval tasks.BudApproval) (tasks.Agent, error) {
	var bud tasks.Agent
	path := "/v1/conversations/" + url.PathEscape(chat) + "/bud-proposals/" + url.PathEscape(turn)
	if approval == "" {
		err := c.call(ctx, http.MethodPost, path, "", nil, &bud)
		return bud, err
	}
	err := c.call(ctx, http.MethodPost, path+"/approval", "", budApprovalRequest{Approval: approval}, &bud)
	var response *HTTPError
	if errors.As(err, &response) && response.Status == http.StatusNotFound && response.Message == "Buddy server returned 404 Not Found" {
		return tasks.Agent{}, &HTTPError{Status: http.StatusNotFound, Message: "Update the Buddy server to approve this draft."}
	}
	return bud, err
}
