package automations

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestGitHubWritesUseFixedEndpointsAndReviewedCredential(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{})
	input := tasks.ProviderCredentials{
		ProviderConnection: tasks.ProviderConnection{Source: tasks.GitHubCI, Name: "Work", Account: "octocat", Collaboration: true},
		Identity:           "42", Secret: "fixture-token",
	}
	connection, err := store.SaveProviderConnection(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.ProviderCredential(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, path, link string
		body             map[string]any
	}{
		{"issue", "/issues", "/issues/8", map[string]any{"title": "Check tests", "body": "Test report"}},
		{"comment", "/issues/7/comments", "/pull/7#issuecomment-8", map[string]any{"body": "Test report"}},
		{"review", "/pulls/7/reviews", "/pull/7#pullrequestreview-8", map[string]any{"body": "Test report", "event": "COMMENT"}},
		{"draft_pull", "/pulls", "/pull/8", map[string]any{"title": "Check tests", "body": "Test report", "head": "feature/tests", "base": "main", "draft": true}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			link := "https://github.com/org/repo" + tc.link
			service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Host != "api.github.com" || req.URL.Path != "/repos/org/repo"+tc.path ||
					req.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Fatalf("unexpected GitHub request: %s %s", req.Method, req.URL)
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil || !maps.Equal(body, tc.body) {
					t.Fatalf("unexpected body: %+v, %v", body, err)
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{},
					Body: io.NopCloser(strings.NewReader(`{"html_url":"` + link + `"}`))}, nil
			})
			proposal := tasks.GitHubWrite{ConnectionID: connection.ID, Repo: "org/repo", Kind: tc.kind,
				Number: 7, Title: "Check tests", Body: "Test report", Head: "feature/tests", Base: "main"}
			if result, err := service.WriteGitHub(ctx, proposal, saved.WriteRevision); err != nil || result.URL != link {
				t.Fatalf("write result: %+v, %v", result, err)
			}
		})
	}
	input.ID, input.Secret = connection.ID, "replaced-token"
	if _, err := store.SaveProviderConnection(ctx, input); err != nil {
		t.Fatal(err)
	}
	service.client.Transport = providerTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("an old approval sent a write after reconnect")
		return nil, nil
	})
	if _, err := service.WriteGitHub(ctx, tasks.GitHubWrite{ConnectionID: input.ID, Repo: "org/repo", Kind: "comment",
		Number: 7, Body: "Stale approval"}, saved.WriteRevision); err == nil {
		t.Fatal("reused a GitHub approval with a new credential")
	}
}
