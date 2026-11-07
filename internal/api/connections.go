package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"buddy/internal/automations"
	"buddy/internal/tasks"
)

const IntegrationsProtocol = 35
const IntegrationSignInProtocol = 53
const connectionTimeout = 45 * time.Second

type ConnectionSetup = automations.ConnectionSetup
type ConnectionCatalog = automations.ConnectionCatalog
type ScopeOption = automations.ScopeOption
type GoogleOAuthClient = automations.GoogleOAuthClient

func integrationRoutes(mux *http.ServeMux, store *tasks.Store, service *automations.Service) {
	mux.HandleFunc("GET /v1/integrations", func(w http.ResponseWriter, r *http.Request) {
		catalog, err := service.ConnectionCatalog(r.Context())
		result(w, http.StatusOK, catalog, err)
	})
	mux.HandleFunc("GET /v1/integrations/gmail/oauth-client", func(w http.ResponseWriter, r *http.Request) {
		client, err := service.GoogleOAuthClient(r.Context())
		result(w, http.StatusOK, client, err)
	})
	mux.HandleFunc("PUT /v1/integrations/gmail/oauth-client", func(w http.ResponseWriter, r *http.Request) {
		var client GoogleOAuthClient
		if err := decode(w, r, &client, 4096); err != nil {
			result(w, 0, nil, err)
			return
		}
		result(w, http.StatusNoContent, nil, service.SaveGoogleOAuthClient(r.Context(), client))
	})
	connect := func(w http.ResponseWriter, r *http.Request) {
		var setup ConnectionSetup
		if err := decode(w, r, &setup, 24<<10); err != nil {
			result(w, 0, nil, err)
			return
		}
		ctx, cancel := providerRequest(w, r)
		defer cancel()
		connection, err := service.Connect(ctx, r.PathValue("id"), setup)
		result(w, http.StatusOK, connection, err)
	}
	mux.HandleFunc("POST /v1/integrations", connect)
	mux.HandleFunc("PUT /v1/integrations/{id}", connect)
	mux.HandleFunc("DELETE /v1/integrations/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, http.StatusOK, nil, store.DisconnectProvider(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /v1/integrations/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := providerRequest(w, r)
		defer cancel()
		connection, err := service.CheckConnection(ctx, r.PathValue("id"))
		result(w, http.StatusOK, connection, err)
	})
	mux.HandleFunc("GET /v1/integrations/{id}/sources", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := providerRequest(w, r)
		defer cancel()
		options, err := service.ConnectionScopes(ctx, r.PathValue("id"))
		result(w, http.StatusOK, options, err)
	})
}

func providerRequest(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(connectionTimeout + time.Second))
	return context.WithTimeout(r.Context(), connectionTimeout)
}

func (c *Client) Integrations(ctx context.Context) (ConnectionCatalog, error) {
	var out ConnectionCatalog
	err := c.call(ctx, http.MethodGet, "/v1/integrations", "", nil, &out)
	return out, err
}

func (c *Client) GoogleOAuthClient(ctx context.Context) (GoogleOAuthClient, error) {
	var out GoogleOAuthClient
	err := c.call(ctx, http.MethodGet, "/v1/integrations/gmail/oauth-client", "", nil, &out)
	return out, err
}

func (c *Client) SaveGoogleOAuthClient(ctx context.Context, id, secret string) error {
	return c.call(ctx, http.MethodPut, "/v1/integrations/gmail/oauth-client", "", GoogleOAuthClient{ID: id, Secret: secret}, nil)
}

func (c *Client) ConnectProvider(ctx context.Context, id string, setup ConnectionSetup) (tasks.ProviderConnection, error) {
	method, path := http.MethodPost, "/v1/integrations"
	if id != "" {
		method, path = http.MethodPut, path+"/"+url.PathEscape(id)
	}
	var out tasks.ProviderConnection
	err := c.callWithTimeout(ctx, connectionTimeout+2*time.Second, method, path, "", setup, &out)
	return out, err
}

func (c *Client) DisconnectProvider(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/integrations/"+url.PathEscape(id), "", nil, nil)
}

func (c *Client) CheckProvider(ctx context.Context, id string) (tasks.ProviderConnection, error) {
	var out tasks.ProviderConnection
	err := c.callWithTimeout(ctx, connectionTimeout+2*time.Second, http.MethodPost,
		"/v1/integrations/"+url.PathEscape(id)+"/check", "", nil, &out)
	return out, err
}

func (c *Client) ProviderScopes(ctx context.Context, id string) ([]ScopeOption, error) {
	var out []ScopeOption
	err := c.callWithTimeout(ctx, connectionTimeout+2*time.Second, http.MethodGet,
		"/v1/integrations/"+url.PathEscape(id)+"/sources", "", nil, &out)
	return out, err
}
