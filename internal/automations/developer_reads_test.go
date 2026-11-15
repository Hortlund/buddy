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

func TestDeveloperGitHubReadsPaginationAndRejectsMovingPR(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{})
	connection, err := store.SaveProviderConnection(ctx, tasks.ProviderCredentials{ProviderConnection: tasks.ProviderConnection{Source: tasks.GitHubCI, Name: "Work"}, Identity: "work", Secret: "token"})
	if err != nil {
		t.Fatal(err)
	}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	moving, reads := false, 0
	patch := strings.Repeat("λ", 12040)
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer token" || req.URL.Host != "api.github.com" || req.Method != "GET" {
			t.Fatalf("wrong GitHub scope: %s", req.URL)
		}
		var body any
		switch req.URL.Path {
		case "/repos/org/repo/pulls/7":
			reads++
			current := head
			if moving && reads%2 == 0 {
				current = strings.Repeat("c", 40)
			}
			body = map[string]any{"number": 7, "changed_files": 21, "head": map[string]string{"sha": current}, "base": map[string]string{"sha": base}}
		case "/repos/org/repo/pulls/7/files":
			if req.URL.Query().Get("per_page") == "20" {
				files := []githubChangedFile{}
				count := 20
				if req.URL.Query().Get("page") == "2" {
					count = 1
				}
				for i := 0; i < count; i++ {
					files = append(files, githubChangedFile{Name: fmt.Sprintf("file%d.go", i)})
				}
				body = files
			} else {
				body = []githubChangedFile{{Name: "main.go", Patch: patch}}
			}
		case "/repos/org/repo/contents/main.go":
			if req.URL.Query().Get("ref") != head {
				t.Fatal("source ref lost")
			}
			body = map[string]string{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("package main"))}
		case "/repos/org/repo/actions/runs/12/jobs":
			body = map[string]any{"total_count": 1, "jobs": []any{map[string]any{"name": "test", "conclusion": "failure", "steps": []any{map[string]string{"name": "Unit tests", "conclusion": "failure"}}}}}
		default:
			t.Fatalf("unexpected read: %s", req.URL)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	query := tasks.GitHubQuery{ConnectionID: connection.ID, Repo: "org/repo", Kind: "pull_files", Number: 7, Ref: head}
	first, err := service.ReadGitHub(ctx, query)
	if err != nil || len(first.Items) != 20 || first.NextPage != 2 || first.HeadSHA != head || first.BaseSHA != base {
		t.Fatalf("first file page: %+v, %v", first, err)
	}
	query.Page = first.NextPage
	last, err := service.ReadGitHub(ctx, query)
	if err != nil || len(last.Items) != 1 || last.More {
		t.Fatalf("last file page: %+v, %v", last, err)
	}
	query.Kind, query.Path, query.Page = "pull_file", "main.go", 0
	first, err = service.ReadGitHub(ctx, query)
	if err != nil || len([]rune(first.Patch)) != 12000 || first.NextPage != 2 {
		t.Fatalf("first patch page: %d, %v", len(first.Patch), err)
	}
	query.Page = 2
	last, err = service.ReadGitHub(ctx, query)
	if err != nil || first.Patch+last.Patch != patch || last.More {
		t.Fatal("patch pagination lost data", err)
	}
	query.Kind, query.Page = "file", 0
	file, err := service.ReadGitHub(ctx, query)
	if err != nil || file.Patch != "package main" {
		t.Fatalf("source read: %+v, %v", file, err)
	}
	query.Kind, query.RunID = "jobs", 12
	jobs, err := service.ReadGitHub(ctx, query)
	if err != nil || len(jobs.Items) != 1 || !strings.Contains(jobs.Items[0].Detail, "Unit tests ·  failure") {
		t.Fatalf("CI steps: %+v, %v", jobs, err)
	}
	query.Kind, query.Ref = "pull_files", strings.Repeat("d", 40)
	if _, err := service.ReadGitHub(ctx, query); err == nil {
		t.Fatal("stale requested head accepted")
	}
	query.Ref, reads, moving = head, 0, true
	if _, err := service.ReadGitHub(ctx, query); err == nil {
		t.Fatal("force-push during read was accepted")
	}
}

func TestGmailThreadPagesAndSearchContinuation(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, localconfig.StandingConfig{})
	connection, err := store.SaveProviderConnection(ctx, tasks.ProviderCredentials{ProviderConnection: tasks.ProviderConnection{Source: tasks.GmailLabel, Name: "Inbox", Account: "work@example.com"}, Identity: "work", Secret: "refresh", ClientID: "client"})
	if err != nil {
		t.Fatal(err)
	}
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/token":
			body = `{"access_token":"access"}`
		case "/gmail/v1/users/me/threads/ab12":
			if req.URL.Query().Get("format") != "full" || req.Header.Get("Authorization") != "Bearer access" {
				t.Fatal("thread scope or format lost")
			}
			messages := make([]map[string]any, 0, 7)
			for _, n := range []int{12, 7, 10, 6, 11, 8, 9} {
				messages = append(messages, map[string]any{"id": fmt.Sprintf("a%d", n), "threadId": "ab12", "internalDate": fmt.Sprint(n), "payload": map[string]any{"mimeType": "text/plain", "body": map[string]string{"data": base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("Message %d", n)))}}})
			}
			data, _ := json.Marshal(map[string]any{"id": "ab12", "messages": messages})
			body = string(data)
		case "/gmail/v1/users/me/messages":
			if req.URL.Query().Get("pageToken") != "opaque+/next" || req.URL.Query().Get("q") != "in:inbox" {
				t.Fatal("search continuation changed")
			}
			body = `{"messages":[],"nextPageToken":"opaque-last"}`
		default:
			t.Fatalf("unexpected Gmail read %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	query := tasks.ConnectionQuery{ConnectionID: connection.ID, Source: tasks.GmailLabel, Kind: "thread", ItemID: "ab12"}
	first, err := service.ReadConnection(ctx, query)
	if err != nil || len(first.Items) != 5 || first.NextPage != 2 || first.ThreadID != "ab12" || first.Items[0].Body != "Message 8" || first.Items[4].Body != "Message 12" {
		t.Fatalf("thread first page: %+v, %v", first, err)
	}
	query.Page = first.NextPage
	last, err := service.ReadConnection(ctx, query)
	if err != nil || len(last.Items) != 2 || last.More || last.Items[0].Body != "Message 6" || last.Items[1].Body != "Message 7" {
		t.Fatalf("older messages: %+v, %v", last, err)
	}
	query.Kind, query.PageToken = "search", "opaque+/next"
	search, err := service.ReadConnection(ctx, query)
	if err != nil || search.NextPageToken != "opaque-last" {
		t.Fatalf("search cursor: %+v, %v", search, err)
	}
}
