package automations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestGitHubWebhookRequiresSignatureAndDeduplicates(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateWatch(ctx, tasks.Watch{
		Name: "CI", Source: tasks.GitHubCI, Scope: "acme/repo",
		ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchDigest, Budget: 5,
	}); err != nil {
		t.Fatal(err)
	}
	const secret = "isolated-test-secret"
	service := New(store, localconfig.StandingConfig{GitHubWebhookSecret: secret})
	body := `{"action":"completed","repository":{"full_name":"Acme/Repo"},
		"workflow_run":{"id":123,"run_attempt":1,"name":"Build","conclusion":"failure"}}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for _, tc := range []struct {
		signature string
		status    int
	}{{"", 401}, {signature, 204}, {signature, 204}} {
		req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "workflow_run")
		req.Header.Set("X-Hub-Signature-256", tc.signature)
		res := httptest.NewRecorder()
		service.GitHubWebhook(res, req)
		if res.Code != tc.status {
			t.Fatalf("webhook status %d, want %d", res.Code, tc.status)
		}
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].State != "digest" || inbox[0].Signal.Title != "Build failed" {
		t.Fatalf("inbox %+v: %v", inbox, err)
	}
}

func TestGitHubResumesSplitSearchAndRecoversReruns(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	watch, err := store.CreateWatch(ctx, tasks.Watch{Name: "CI", Source: tasks.GitHubCI, Scope: "acme/repo",
		ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchDigest, Budget: 5})
	if err != nil {
		t.Fatal(err)
	}
	service := New(store, localconfig.StandingConfig{GitHubToken: "fixture-token"})
	var searches, attempts int
	var firstRange, leftRange, rightRange string
	now := watch.CreatedAt.Add(time.Second).Format(time.RFC3339Nano)
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/repos/acme/repo/actions/runs":
			searches++
			query := req.URL.Query()
			window := query.Get("created")
			if query.Has("status") {
				t.Fatal("status filter hides successful reruns")
			}
			switch {
			case searches == 1:
				firstRange = window
				body = `{"total_count":1000}`
			case searches <= 7:
				leftRange = window
				if searches < 7 {
					body = `{"total_count":500,"workflow_runs":[` + strings.Repeat(`{},`, 99) + `{}]}`
				} else {
					body = `{"total_count":500}`
				}
			case searches <= 12:
				rightRange = window
				body = `{"total_count":501,"workflow_runs":[` + strings.Repeat(`{},`, 99) + `{}]}`
			default:
				if window != rightRange || query.Get("page") != "6" {
					t.Fatalf("did not resume the split window: %s", req.URL)
				}
				body = `{"total_count":501,"workflow_runs":[{"id":123,"run_attempt":2,"name":"Build","conclusion":"success","updated_at":"` + now + `"}]}`
			}
		case "/repos/acme/repo/actions/runs/123/attempts/1":
			attempts++
			body = `{"id":123,"run_attempt":1,"name":"Build","conclusion":"failure","updated_at":"` + now + `"}`
		default:
			t.Fatalf("unexpected GitHub request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if err := service.poll(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := store.StandingPoll(ctx, watch.Source, watch.Scope)
	var progress githubProgress
	if err != nil || json.Unmarshal([]byte(p.Cursor), &progress) != nil || searches != 10 || len(progress.Windows) != 1 || progress.Windows[0].Page != 4 {
		t.Fatalf("expected saved pagination: %d, %+v, %v", searches, p, err)
	}
	if firstRange == leftRange || leftRange == rightRange || firstRange == rightRange {
		t.Fatalf("oversized range was not split: %q / %q / %q", firstRange, leftRange, rightRange)
	}
	if err := service.poll(ctx); err != nil {
		t.Fatal(err)
	}
	p, err = store.StandingPoll(ctx, watch.Source, watch.Scope)
	if _, parseErr := time.Parse(time.RFC3339Nano, p.Cursor); err != nil || p.Problem != "" || attempts != 1 || parseErr != nil {
		t.Fatalf("catch-up did not finish: %+v, %v", p, err)
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].Signal.Key != "123:1" {
		t.Fatalf("failed attempt not recovered: %+v, %v", inbox, err)
	}
}
