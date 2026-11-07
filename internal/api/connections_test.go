package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"buddy/internal/automations"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestIntegrationOwnerCanConnectTestDisconnectAndReconnect(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/alerts" || r.Header.Get("Authorization") != "Bearer test-connection-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"alerts":[]}}`)
	}))
	defer upstream.Close()
	worker := tasks.NewWorker(store, nil, nil)
	service := automations.New(store, localconfig.StandingConfig{})
	server := startTestServer(t, dir, Handler(token, store, worker, nil, service))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()

	setup := ConnectionSetup{Name: "Metrics", Source: tasks.PrometheusAlerts, Endpoint: upstream.URL, Token: "test-connection-token"}
	connection, err := client.ConnectProvider(ctx, "", setup)
	if err != nil || !connection.Connected || connection.ID == "" {
		t.Fatalf("connect: %+v, %v", connection, err)
	}
	if checked, err := client.CheckProvider(ctx, connection.ID); err != nil || checked.Problem != "" {
		t.Fatalf("test connection: %+v, %v", checked, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/integrations", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK || strings.Contains(string(data), setup.Token) {
		t.Fatal("connection listing failed or exposed the token")
	}
	if err := client.CreateWatch(ctx, tasks.Watch{
		Source: setup.Source, ConnectionID: connection.ID, Name: "Firing alerts", Budget: 15,
		ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.DisconnectProvider(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	watches, err := client.Watches(ctx)
	if err != nil || len(watches) != 1 || watches[0].Enabled || watches[0].ConnectionID != connection.ID {
		t.Fatalf("disconnect lost or left an active watch: %+v, %v", watches, err)
	}
	var httpErr *HTTPError
	if err := client.SetWatchEnabled(ctx, watches[0].ID, true); !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("watch resumed while disconnected: %v", err)
	}
	reconnected, err := client.ConnectProvider(ctx, connection.ID, setup)
	if err != nil || !reconnected.Connected || reconnected.ID != connection.ID {
		t.Fatalf("reconnect: %+v, %v", reconnected, err)
	}
	if err := client.SetWatchEnabled(ctx, watches[0].ID, true); err != nil {
		t.Fatal(err)
	}
}
