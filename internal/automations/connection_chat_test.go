package automations

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestConnectionChatReadsProviderDataWithSelectedCredential(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{})
	connections := make(map[string]tasks.ProviderConnection)
	for _, c := range []tasks.ProviderCredentials{
		{ProviderConnection: tasks.ProviderConnection{Source: tasks.GmailLabel, Name: "Inbox", Account: "work@example.com"}, Secret: "gmail-refresh", ClientID: "gmail-client"},
		{ProviderConnection: tasks.ProviderConnection{Source: tasks.GrafanaAlerts, Name: "Production", Endpoint: "https://grafana.example.test/ops"}, Secret: "grafana-read"},
		{ProviderConnection: tasks.ProviderConnection{Source: tasks.PrometheusAlerts, Name: "Metrics", Endpoint: "https://prometheus.example.test"}, Secret: "prometheus-read"},
	} {
		c.Identity = c.Name
		saved, err := store.SaveProviderConnection(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		connections[c.Source] = saved
	}
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		expectAuth, body := "", ""
		switch req.URL.Host + req.URL.Path {
		case "oauth2.googleapis.com/token":
			if err := req.ParseForm(); err != nil || req.Method != http.MethodPost || req.Form.Get("refresh_token") != "gmail-refresh" {
				t.Fatalf("unexpected Gmail refresh: %v", err)
			}
			body = `{"access_token":"gmail-access"}`
		case "gmail.googleapis.com/gmail/v1/users/me/messages":
			expectAuth = "gmail-access"
			if req.URL.Query().Get("q") != "is:unread from:alice@example.com" {
				t.Fatal("Gmail search query was lost")
			}
			body = `{"nextPageToken":"more","messages":[{"id":"ab12","threadId":"th1"}]}`
		case "gmail.googleapis.com/gmail/v1/users/me/messages/ab12":
			expectAuth = "gmail-access"
			body = fmt.Sprintf(`{"id":"ab12","threadId":"th1","labelIds":["UNREAD"],"snippet":"Short preview","payload":{
				"mimeType":"text/html","headers":[{"name":"Subject","value":"Invoice"},{"name":"From","value":"Alice <alice@example.com>"}],
				"body":{"data":%q}}}`, base64.RawURLEncoding.EncodeToString([]byte("<head><style>secret-style</style></head><p>Invoice total is &euro;42.</p><script>secret-script</script>")))
		case "gmail.googleapis.com/gmail/v1/users/me/messages/older", "gmail.googleapis.com/gmail/v1/users/me/messages/newer":
			expectAuth = "gmail-access"
			body = fmt.Sprintf(`{"labelIds":["Label_42"],"internalDate":"1700000000000","payload":{"headers":[
				{"name":"Subject","value":%q},{"name":"From","value":%q}]}}`,
				strings.Repeat("Long subject ", 25)+"INVOICE", `"`+strings.Repeat("Long display name ", 25)+`" <alice@example.com>`)
		case "gmail.googleapis.com/gmail/v1/users/me/labels":
			expectAuth = "gmail-access"
			body = `{"labels":[{"id":"Label_42","name":"Finance/Invoices"}]}`
		case "gmail.googleapis.com/gmail/v1/users/me/profile":
			expectAuth = "gmail-access"
			body = `{"historyId":"1000"}`
		case "grafana.example.test/ops/api/search":
			expectAuth = "grafana-read"
			body = `[{"uid":"node-overview","orgId":17,"title":"Node overview","folderTitle":"Ops","url":"https://untrusted.example"}]`
		case "grafana.example.test/ops/api/alertmanager/grafana/api/v2/alerts":
			expectAuth = "grafana-read"
			body = `[{"status":{"state":"active"},"labels":{"alertname":"CPUHigh","severity":"critical"},"annotations":{"summary":"CPU above 90%"}}]`
		case "prometheus.example.test/api/v1/query":
			expectAuth = "prometheus-read"
			if req.URL.Query().Get("timeout") != "5s" {
				t.Fatal("PromQL expression or timeout was lost")
			}
			switch req.URL.Query().Get("query") {
			case "up":
				body = `{"status":"success","warnings":["partial data"],"data":{"resultType":"vector","result":[
					{"metric":{"__name__":"duration"},"histogram":[1793980000,{"count":"12","sum":"25","buckets":[]}]},
					{"metric":{"__name__":"up","job":"api"},"value":[1793980000,"1"]}]}}`
			case "empty":
				body = `{"status":"success","infos":["samples omitted"],"data":{"resultType":"vector","result":[]}}`
			default:
				t.Fatal("PromQL expression was lost")
			}
		default:
			t.Fatalf("unexpected provider request %s", req.URL)
		}
		if expectAuth != "" && (req.Header.Get("Authorization") != "Bearer "+expectAuth || req.Method != http.MethodGet) {
			t.Fatalf("incorrect credential or method for %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	for _, fixture := range []struct {
		query tasks.ConnectionQuery
		want  string
	}{
		{tasks.ConnectionQuery{Source: tasks.GmailLabel, Kind: "search", Query: "is:unread from:alice@example.com"}, "#all/th1"},
		{tasks.ConnectionQuery{Source: tasks.GmailLabel, Kind: "message", ItemID: "ab12"}, "Invoice total is €42."},
		{tasks.ConnectionQuery{Source: tasks.GrafanaAlerts, Kind: "dashboards", Query: "Node"}, "https://grafana.example.test/ops/d/node-overview?orgId=17"},
		{tasks.ConnectionQuery{Source: tasks.GrafanaAlerts, Kind: "alerts"}, "critical"},
		{tasks.ConnectionQuery{Source: tasks.PrometheusAlerts, Kind: "query", Query: "up"}, `"value":"1"`},
		{tasks.ConnectionQuery{Source: tasks.PrometheusAlerts, Kind: "query", Query: "empty"}, "Prometheus returned no samples."},
	} {
		query := fixture.query
		query.ConnectionID = connections[query.Source].ID
		card, err := service.ReadConnection(ctx, query)
		raw, _ := json.Marshal(card)
		if err != nil || !strings.Contains(string(raw), fixture.want) || strings.Contains(string(raw), "untrusted.example") ||
			strings.Contains(string(raw), "secret-script") || strings.Contains(string(raw), "secret-style") {
			t.Fatalf("%s %s: %s, %v", query.Source, query.Kind, raw, err)
		}
		if query.Query == "up" && (len(card.Items) != 2 || card.Items[0].Badge != "Histogram" || len(card.Warnings) != 1) {
			t.Fatalf("histogram or warning lost: %+v", card)
		}
		if query.Query == "empty" && (len(card.Warnings) != 1 || card.Warnings[0] != "samples omitted") {
			t.Fatalf("query notices lost: %+v", card)
		}
	}
	id, label, err := service.ResolveWatchScope(ctx, tasks.Watch{
		Source: tasks.GmailLabel, ConnectionID: connections[tasks.GmailLabel].ID, Scope: "finance/invoices",
	})
	if err != nil || id != "Label_42" || label != "Finance/Invoices" {
		t.Fatalf("resolved Gmail label: %q %q, %v", id, label, err)
	}
	watch := tasks.Watch{Source: tasks.GmailLabel, ConnectionID: connections[tasks.GmailLabel].ID, Scope: id}
	if err := service.PrepareWatchStart(ctx, &watch); err != nil || watch.GmailHistory != "1000" {
		t.Fatalf("missing Gmail activation cursor: %q, %v", watch.GmailHistory, err)
	}
	watch.ConversationID, watch.Name, watch.Mode, watch.Budget = tasks.DefaultConversationID, "Invoices", tasks.WatchDigest, 15
	watch.Filters = []tasks.WatchFilter{{Field: "sender", Value: "alice@example.com"}, {Field: "subject", Value: "invoice"}}
	saved, err := store.CreateWatch(ctx, watch)
	if err != nil {
		t.Fatal(err)
	}
	for message, history := range map[string]string{"older": "999", "newer": "1001"} {
		if err := service.gmailMessage(ctx, "gmail-access", saved, message, history, true); err != nil {
			t.Fatal(err)
		}
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].Signal.Key != "newer" {
		t.Fatalf("incorrect Gmail filter or history boundary: %+v, %v", inbox, err)
	}
}
