package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

const mobileRedeem = "/v1/mobile/pair"

func mobileRoutes(owner *http.ServeMux, store *tasks.Store, pushTopic string) *http.ServeMux {
	owner.HandleFunc("POST /v1/mobile/pairings", func(w http.ResponseWriter, r *http.Request) {
		pair, err := store.CreateMobilePairing(r.Context())
		result(w, http.StatusCreated, pair, err)
	})
	owner.HandleFunc("GET /v1/mobile/clients", func(w http.ResponseWriter, r *http.Request) {
		items, err := store.MobileClients(r.Context())
		result(w, http.StatusOK, items, err)
	})
	owner.HandleFunc("DELETE /v1/mobile/clients/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusNoContent, nil, store.RevokeMobileClient(r.Context(), r.PathValue("id")))
	})

	mobile := http.NewServeMux()
	// The phone can participate in conversations and review work. New owner
	// endpoints stay private unless deliberately added to this list.
	for _, pattern := range []string{
		"GET " + voiceKeyPath,
		"GET " + voiceCallPath,
		"GET /v1/workspace",
		"GET /v1/agents/{id}/workspace/item",
		"GET /v1/conversations/{id}",
		"GET /v1/tasks/{task}/generated-image",
		"POST /v1/conversations/{id}/messages",
		"PUT /v1/conversations/{id}/messages/{message}/reaction",
		"POST /v1/conversations/{id}/read",
		"POST /v1/conversations/{id}/cards/{card}/answer",
		"POST /v1/conversations/{id}/bud-proposals/{turn}",
		budApprovalRoute,
		"POST /v1/conversations/{id}/watch-drafts/{task}/activate",
		"GET /v1/inbox",
		"GET /v1/inbox/{id}",
		"PATCH /v1/inbox/{id}",
		"GET /v1/work",
		"GET /v1/work/history",
		"GET /v1/work/{job}",
		"POST /v1/work/{job}/cancel",
		"GET /v1/work/{job}/artifacts/{file}",
		"GET /v1/work/{job}/frame",
		"POST /v1/work/{job}/control",
		"GET /v1/hosts",
		"GET /v1/projects",
		"GET /v1/conversations/{chat}/work/{job}",
		"POST /v1/conversations/{chat}/work-inputs/{input}/answer",
	} {
		mobile.Handle(pattern, owner)
	}
	mobile.HandleFunc("GET /v1/mobile/notifications", func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, struct {
			Available bool   `json:"available"`
			BundleID  string `json:"bundle_id,omitempty"`
		}{pushTopic != "", pushTopic})
	})
	mobile.HandleFunc("PUT /v1/mobile/notifications", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token string `json:"token"`
			Work  bool   `json:"work"`
		}
		if err := decode(w, r, &input, 2048); err != nil {
			result(w, 0, nil, err)
			return
		}
		if pushTopic == "" && input.Token != "" {
			result(w, 0, nil, &tasks.InputError{Message: "This Buddy server hasn't set up iPhone notifications."})
			return
		}
		id := tasks.MobileClientFromContext(r.Context())
		result(w, http.StatusOK, nil, store.SetMobileNotifications(r.Context(), id, input.Token, input.Work))
	})
	mobile.HandleFunc("DELETE /v1/mobile/session", func(w http.ResponseWriter, r *http.Request) {
		id := tasks.MobileClientFromContext(r.Context())
		result(w, http.StatusNoContent, nil, store.RevokeMobileClient(r.Context(), id))
	})
	return mobile
}

func redeemMobilePairing(w http.ResponseWriter, r *http.Request, store *tasks.Store) {
	var input struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := decode(w, r, &input, 1024); err != nil {
		result(w, 0, nil, err)
		return
	}
	credential, err := store.PairMobileClient(r.Context(), input.Code, input.Name)
	if errors.Is(err, tasks.ErrNotFound) {
		write(w, http.StatusGone, errorBody{"This pairing code expired or was already used. Generate a new one in Buddy on your Mac."})
		return
	}
	result(w, http.StatusCreated, credential, err)
}

func (c *Client) CreateMobilePairing(ctx context.Context, server string) (string, time.Time, error) {
	if err := localconfig.ValidateServerURL(server); err != nil {
		return "", time.Time{}, err
	}
	cert, err := c.certificate()
	if err != nil {
		return "", time.Time{}, err
	}
	block, _ := pem.Decode([]byte(cert))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", time.Time{}, errors.New("Buddy couldn't read its server certificate")
	}
	var pair tasks.MobilePairing
	if err := c.call(ctx, http.MethodPost, "/v1/mobile/pairings", "", nil, &pair); err != nil {
		return "", time.Time{}, err
	}
	pin := sha256.Sum256(block.Bytes)
	query := url.Values{"v": {"1"}, "server": {strings.TrimRight(server, "/")}, "pin": {hex.EncodeToString(pin[:])}, "code": {pair.Code}}
	return "buddy-pair://connect?" + query.Encode(), pair.ExpiresAt, nil
}

func (c *Client) MobileClients(ctx context.Context) ([]tasks.MobileClient, error) {
	var clients []tasks.MobileClient
	err := c.call(ctx, http.MethodGet, "/v1/mobile/clients", "", nil, &clients)
	return clients, err
}

func (c *Client) RevokeMobileClient(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/mobile/clients/"+url.PathEscape(id), "", nil, nil)
}
