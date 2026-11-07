package automations

import (
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

type providerTestTransport func(*http.Request) (*http.Response, error)

func (t providerTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return t(req) }

func TestGmailCatchupAndExpiredCursor(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const label = "Label_23"
	if _, err := store.CreateWatch(ctx, tasks.Watch{Name: "Triage", Source: tasks.GmailLabel,
		Scope: label, ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchDigest, Budget: 5}); err != nil {
		t.Fatal(err)
	}
	service := New(store, localconfig.StandingConfig{GmailRefreshToken: "fixture-refresh"})
	var failMetadata bool
	var metadataReads int
	service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		query := req.URL.Query()
		switch req.URL.Path {
		case "/token":
			if err := req.ParseForm(); err != nil || req.PostForm.Get("refresh_token") != "fixture-refresh" {
				t.Fatal("missing OAuth refresh form")
			}
			body = `{"access_token":"fixture-access"}`
		case "/gmail/v1/users/me/profile":
			body = `{"historyId":"102"}`
		case "/gmail/v1/users/me/history":
			if query.Get("labelId") != label {
				t.Fatal("history request escaped the chosen label")
			}
			if query.Get("startHistoryId") == "101" {
				status = http.StatusNotFound
			} else {
				body = `{"historyId":"103","history":[{"labelsAdded":[{"message":{"id":"m1"},"labelIds":["Label_23"]}]}]}`
			}
		case "/gmail/v1/users/me/messages":
			if query.Get("labelIds") != label || query.Get("q") != "" {
				t.Fatal("resync must include older messages that acquired the label during downtime")
			}
			body = `{"messages":[{"id":"m1"}]}`
		case "/gmail/v1/users/me/messages/m1":
			metadataReads++
			if query.Get("format") != "metadata" || req.Header.Get("Authorization") != "Bearer fixture-access" {
				t.Fatal("missing authorized metadata request")
			}
			if failMetadata {
				status = http.StatusServiceUnavailable
			} else {
				body = `{"labelIds":["Label_23"],"snippet":"Can you reply?","payload":{"headers":[{"Name":"Subject","Value":"Project mail"}]}}`
			}
		default:
			t.Fatalf("unexpected provider route %s", req.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	p, err := store.StandingPoll(ctx, tasks.GmailLabel, label)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStandingPoll(ctx, p, "101", ""); err != nil {
		t.Fatal(err)
	}
	failMetadata = true
	if err := service.poll(ctx); err != nil {
		t.Fatal(err)
	}
	p, err = store.StandingPoll(ctx, tasks.GmailLabel, label)
	var progress gmailProgress
	if err != nil || p.Problem == "" || json.Unmarshal([]byte(p.Cursor), &progress) != nil || !progress.Resync || progress.History != "102" {
		t.Fatalf("lost resync checkpoint on provider failure: %+v, %v", p, err)
	}
	failMetadata = false
	for range 2 {
		if err := service.poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	p, err = store.StandingPoll(ctx, tasks.GmailLabel, label)
	if err != nil || p.Cursor != "103" || p.Problem != "" {
		t.Fatalf("resync did not recover: %+v, %v", p, err)
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].Signal.Title != "Project mail" || metadataReads != 2 {
		t.Fatalf("inbox %+v, metadata reads %d, %v", inbox, metadataReads, err)
	}
}

func TestGmailPersistsPaginationAcrossPolls(t *testing.T) {
	for _, path := range []string{"history", "messages"} {
		t.Run(path, func(t *testing.T) {
			ctx := t.Context()
			store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			watch, err := store.CreateWatch(ctx, tasks.Watch{Name: "Triage", Source: tasks.GmailLabel,
				Scope: "INBOX", ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchDigest, Budget: 5})
			if err != nil {
				t.Fatal(err)
			}
			p := tasks.PollState{Source: watch.Source, Scope: watch.Scope}
			if err := store.RecordStandingPoll(ctx, p, "101", ""); err != nil {
				t.Fatal(err)
			}
			service := New(store, localconfig.StandingConfig{GmailRefreshToken: "fixture-refresh"})
			var pages int
			service.client.Transport = providerTestTransport(func(req *http.Request) (*http.Response, error) {
				body := `{"access_token":"fixture-access"}`
				status := http.StatusOK
				switch route := req.URL.Path; {
				case route == "/token":
				case strings.HasSuffix(route, "/profile"):
					body = `{"historyId":"102"}`
				case path == "messages" && strings.HasSuffix(route, "/history"):
					status = http.StatusNotFound
				case strings.HasSuffix(route, "/"+path):
					if pages > 0 && req.URL.Query().Get("pageToken") != fmt.Sprint(pages) {
						t.Fatalf("replayed a page after %d completed pages", pages)
					}
					pages++
					if pages <= 10 {
						body = fmt.Sprintf(`{"historyId":"103","nextPageToken":"%d"}`, pages)
					} else if path == "history" {
						body = `{"historyId":"103","history":[{"messagesAdded":[{"message":{"id":"late"}}]}]}`
					} else {
						body = `{"messages":[{"id":"late"}]}`
					}
				case strings.HasSuffix(route, "/messages/late"):
					body = `{"labelIds":["INBOX"]}`
				default:
					t.Fatalf("unexpected provider route %s", route)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			for range 2 {
				if err := service.poll(ctx); err != nil {
					t.Fatal(err)
				}
			}
			p, err = store.StandingPoll(ctx, watch.Source, watch.Scope)
			if err != nil || pages != 11 || p.Problem != "" {
				t.Fatalf("pagination did not complete: %d pages, %+v, %v", pages, p, err)
			}
			inbox, err := store.StandingInbox(ctx)
			if err != nil || len(inbox) != 1 || inbox[0].Signal.Key != "late" {
				t.Fatalf("late message missing: %+v, %v", inbox, err)
			}
		})
	}
}
