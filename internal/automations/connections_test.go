package automations

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestNamedGitHubWatchUsesItsConnectionForRunsAndEvidence(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{GitHubToken: "legacy"})
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer named-token" {
			t.Fatalf("named watch sent the wrong credential to %s", req.URL.Path)
		}
		var body string
		switch req.URL.Path {
		case "/user":
			body = `{"id":42,"login":"octocat"}`
		case "/user/repos":
			body = `[{"full_name":"octocat/Project"}]`
		case "/repos/octocat/project/actions/runs":
			at := time.Now().UTC().Format(time.RFC3339Nano)
			body = fmt.Sprintf(`{"total_count":1,"workflow_runs":[{"id":35,"run_attempt":1,"name":"Build","conclusion":"failure","created_at":%q,"updated_at":%q}]}`, at, at)
		case "/repos/octocat/project/actions/runs/35/attempts/1/jobs":
			body = `{"jobs":[{"id":1,"name":"Tests","conclusion":"failure","steps":[{"name":"go test","conclusion":"failure"}]}]}`
		default:
			t.Fatalf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	connection, err := service.Connect(ctx, "", ConnectionSetup{Source: tasks.GitHubCI, Name: "Work", Token: "named-token"})
	if err != nil || connection.Account != "octocat" {
		t.Fatalf("GitHub connection: %+v, %v", connection, err)
	}
	scopes, err := service.ConnectionScopes(ctx, connection.ID)
	if err != nil || len(scopes) != 1 || scopes[0].ID != "octocat/project" {
		t.Fatalf("GitHub repositories: %+v, %v", scopes, err)
	}
	watch := tasks.Watch{Source: tasks.GitHubCI, ConnectionID: connection.ID, Scope: scopes[0].ID,
		Name: "Work CI", ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchDigest, Budget: 5}
	if err := service.ValidateWatchSource(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if err := service.poll(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := store.StandingInbox(ctx)
	if err != nil || len(items) != 1 || items[0].Signal.ConnectionID != connection.ID {
		t.Fatalf("GitHub watch lost the connection: %+v, %v", items, err)
	}
	signal, err := service.Enrich(ctx, items[0].Signal)
	if err != nil || !signal.Enriched || !strings.Contains(signal.Detail, "go test") {
		t.Fatalf("GitHub evidence: %+v, %v", signal, err)
	}
}

func TestGitHubChatUsesSelectedAccountAndFixedReadEndpoints(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{GitHubToken: "legacy"})
	var work tasks.ProviderConnection
	for _, name := range []string{"home", "work"} {
		saved, err := store.SaveProviderConnection(ctx, tasks.ProviderCredentials{
			ProviderConnection: tasks.ProviderConnection{Source: tasks.GitHubCI, Name: name, Account: name},
			Secret:             name + "-token", Identity: name,
		})
		if err != nil {
			t.Fatal(err)
		}
		if name == "work" {
			work = saved
		}
	}
	connections, err := service.GitHubChatConnections(ctx)
	if err != nil || len(connections) != 2 || connections[1].ID != work.ID {
		t.Fatalf("unexpected connected accounts: %+v, %v", connections, err)
	}
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Host != "api.github.com" ||
			req.Header.Get("Authorization") != "Bearer work-token" {
			t.Fatal("GitHub chat request used the wrong endpoint, method, or credential")
		}
		var body string
		switch req.URL.Path {
		case "/repos/org/repo/pulls/7":
			body = `{"number":7,"title":"Fix tests","state":"open","changed_files":2,"head":{"ref":"tests"},"base":{"ref":"main"}}`
		case "/repos/org/repo/pulls/7/files":
			body = `[{"filename":"test.go","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-before\n+after"}]`
		case "/repos/org/repo/issues":
			body = `[{"number":7,"title":"Fix tests","pull_request":{}},{"number":8,"title":"Improve tests","state":"open"}]`
		case "/repos/org/repo/actions/runs":
			if req.URL.Query().Get("branch") != "feature/x" {
				t.Fatal("branch filter was lost")
			}
			body = `{"total_count":1,"workflow_runs":[{"id":12,"name":"CI","head_branch":"feature/x","status":"in_progress"}]}`
		default:
			t.Fatalf("unexpected GitHub chat request %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	query := tasks.GitHubQuery{ConnectionID: work.ID, Repo: "org/repo", Kind: "pull", Number: 7}
	result, err := service.ReadGitHub(ctx, query)
	if err != nil || result.URL != "https://github.com/org/repo/pull/7" || !result.More ||
		!strings.Contains(result.Patch, "-before\n+after") || !strings.Contains(result.Detail, "tests → main") {
		t.Fatalf("pull request preview: %+v, %v", result, err)
	}
	query.Kind = "issues"
	result, err = service.ReadGitHub(ctx, query)
	if err != nil || len(result.Items) != 1 || result.Items[0].URL != "https://github.com/org/repo/issues/8" {
		t.Fatalf("issues preview: %+v, %v", result, err)
	}
	query.Kind, query.Ref = "checks", "feature/x"
	result, err = service.ReadGitHub(ctx, query)
	if err != nil || len(result.Items) != 1 || result.Items[0].Detail != "in progress · feature/x" {
		t.Fatalf("CI preview: %+v, %v", result, err)
	}
	query.Repo = "org/.."
	if _, err := service.ReadGitHub(ctx, query); err == nil {
		t.Fatal("accepted path traversal in repo")
	}
	if err := store.DisconnectProvider(ctx, work.ID); err != nil {
		t.Fatal(err)
	}
	query.Repo = "org/repo"
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("disconnected account made a request to %s", req.URL)
		return nil, nil
	})
	if _, err := service.ReadGitHub(ctx, query); err == nil {
		t.Fatal("read with disconnected account")
	}
}

func TestNamedGmailConnectionsKeepIdenticalLabelAndMessageIDsSeparate(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{})
	oauth := GoogleOAuthClient{ID: "client.apps.googleusercontent.com", Secret: "fixture-client-secret"}
	if err := service.SaveGoogleOAuthClient(ctx, oauth); err != nil {
		t.Fatal(err)
	}
	if public, err := service.GoogleOAuthClient(ctx); err != nil || public.ID != oauth.ID || public.Secret != "" {
		t.Fatal("the saved OAuth client ID was unavailable or exposed its secret")
	}
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		account := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer access-")
		switch req.URL.Path {
		case "/token":
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch req.Form.Get("grant_type") {
			case "authorization_code":
				account = req.Form.Get("code")
				if len(req.Form.Get("code_verifier")) < 43 || req.Form.Get("redirect_uri") != "http://127.0.0.1:1234/callback" {
					t.Fatal("Gmail exchange omitted the loopback redirect or PKCE verifier")
				}
				if req.Form.Get("client_id") != oauth.ID || req.Form.Get("client_secret") != oauth.Secret {
					t.Fatal("Gmail exchange did not reuse the saved OAuth client")
				}
			case "refresh_token":
				account = strings.TrimPrefix(req.Form.Get("refresh_token"), "fixture-refresh-")
			default:
				t.Fatalf("unexpected grant %q", req.Form.Get("grant_type"))
			}
			body = fmt.Sprintf(`{"access_token":"access-%s","refresh_token":"fixture-refresh-%s"}`, account, account)
		case "/v1/userinfo":
			body = fmt.Sprintf(`{"sub":"account-%s"}`, account)
		case "/gmail/v1/users/me/profile":
			body = fmt.Sprintf(`{"emailAddress":"%s@example.test","historyId":"101"}`, account)
		case "/gmail/v1/users/me/labels":
			body = `{"labels":[{"id":"INBOX","name":"Inbox"}]}`
		case "/gmail/v1/users/me/messages":
			body = `{"messages":[{"id":"same-id"}]}`
		case "/gmail/v1/users/me/messages/same-id":
			body = fmt.Sprintf(`{"labelIds":["INBOX"],"snippet":"mailbox %s"}`, account)
		case "/gmail/v1/users/me/history":
			body = `{"historyId":"102"}`
		default:
			t.Fatalf("unexpected request %s", req.URL)
		}
		if account != "work" && account != "home" {
			t.Fatalf("unexpected account for %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	connections := make(map[string]tasks.ProviderConnection)
	for _, account := range []string{"work", "home"} {
		connection, err := service.Connect(ctx, "", ConnectionSetup{Source: tasks.GmailLabel, UseSavedGoogleClient: true,
			ClientID: oauth.ID, Code: account, Verifier: strings.Repeat("x", 48),
			Redirect: "http://127.0.0.1:1234/callback"})
		if err != nil || connection.Account != account+"@example.test" || connection.Name != connection.Account {
			t.Fatalf("connect %s: %+v, %v", account, connection, err)
		}
		connections[account] = connection
		if scopes, err := service.ConnectionScopes(ctx, connection.ID); err != nil || len(scopes) != 1 || scopes[0].ID != "INBOX" {
			t.Fatalf("mailbox labels: %+v, %v", scopes, err)
		}
		if _, err := store.CreateWatch(ctx, tasks.Watch{ConnectionID: connection.ID, Source: tasks.GmailLabel,
			Scope: "INBOX", Name: account, ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchAsk, Budget: 5}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.poll(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := store.StandingInbox(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("matching message IDs were collapsed: %+v, %v", items, err)
	}
	for _, item := range items {
		if item.Signal.ConnectionID != connections[item.Watch].ID ||
			!strings.Contains(item.Signal.Detail, "mailbox "+item.Watch) {
			t.Fatalf("watch used another mailbox's credential or evidence: %+v", item)
		}
	}
	if err := store.DisconnectProvider(ctx, connections["work"].ID); err != nil {
		t.Fatal(err)
	}
	items, err = store.StandingInbox(ctx)
	if err != nil || len(items) != 1 || items[0].Watch != "home" {
		t.Fatalf("disconnect did not dismiss just the disconnected mailbox's approvals: %+v, %v", items, err)
	}
	credential, err := store.ProviderCredential(ctx, connections["work"].ID)
	if err != nil || credential.Connected || credential.Secret != "" || credential.ClientID != "" {
		t.Fatalf("disconnect left an active credential: %v", err)
	}
	if _, err := service.Connect(ctx, connections["work"].ID, ConnectionSetup{Source: tasks.GmailLabel, Name: "Work",
		UseSavedGoogleClient: true, ClientID: oauth.ID, Code: "home", Verifier: strings.Repeat("x", 48),
		Redirect: "http://127.0.0.1:1234/callback"}); err == nil {
		t.Fatal("a different Google account replaced the disconnected connection")
	}
}

func TestMonitoringConnectionsDeliverOnlyNewFiringAlerts(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{})
	newAlert := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/east/api/v1/alerts", "/west/api/v1/alerts":
			if r.Header.Get("Authorization") != "" {
				t.Error("unauthenticated Prometheus received a credential")
			}
			fmt.Fprintf(w, `{"status":"success","data":{"alerts":[
				{"state":"pending","labels":{"alertname":"Waiting"},"activeAt":"2026-10-01T00:00:00Z"},
				{"state":"firing","labels":{"alertname":"Already firing"},"activeAt":"2026-10-01T00:00:00Z"},
				{"state":"firing","labels":{"alertname":"CPU high"},"activeAt":%q}
			]}}`, newAlert)
		case "/grafana/api/alertmanager/grafana/api/v2/alerts":
			if r.Header.Get("Authorization") != "Bearer fixture-grafana" || r.URL.Query().Get("silenced") != "false" {
				t.Error("Grafana request omitted authentication or the firing-alert filter")
			}
			fmt.Fprintf(w, `[{"status":{"state":"active"},"labels":{"alertname":"CPU high"},
				"startsAt":%q,"annotations":{"summary":"A busy node"}}]`, newAlert)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	for _, setup := range []ConnectionSetup{
		{Name: "East", Source: tasks.PrometheusAlerts, Endpoint: provider.URL + "/east"},
		{Name: "West", Source: tasks.PrometheusAlerts, Endpoint: provider.URL + "/west"},
		{Name: "Grafana", Source: tasks.GrafanaAlerts, Endpoint: provider.URL + "/grafana", Token: "fixture-grafana"},
	} {
		connection, err := service.Connect(ctx, "", setup)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(connection)
		if err != nil || strings.Contains(string(encoded), setup.Token) && setup.Token != "" {
			t.Fatal("the public connection response contained credentials")
		}
		if _, err := store.CreateWatch(ctx, tasks.Watch{ConnectionID: connection.ID, Source: connection.Source,
			Name: connection.Name, ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchDigest, Budget: 5}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := service.poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.StandingInbox(ctx)
	if err != nil || len(items) != 3 {
		t.Fatalf("expected one event per monitoring instance: %+v, %v", items, err)
	}
	for _, item := range items {
		if item.Signal.Title != "CPU high is firing" {
			t.Fatalf("received a non-firing alert: %+v", item)
		}
	}
}
