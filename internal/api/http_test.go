package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestRepoCheckOutlivesClient(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 12*time.Second)
	defer stop()
	dir := t.TempDir()
	repo := initRepo(t, dir)
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := tasks.NewWorker(store, tasks.RunRepositoryCheck, nil)
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(workerCtx)
	}()
	defer func() { stopWorker(); <-done }()

	server := startTestServer(t, dir, Handler(token, store, worker, nil))
	defer server.Close()
	tokenFile := filepath.Join(dir, localconfig.TokenName)

	resp, err := server.Client().Get(server.URL + "/v1/tasks")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request status = %d", resp.StatusCode)
	}

	first := NewClient(server.URL, tokenFile)
	requestCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	t.Cleanup(first.Close)
	req := tasks.Request{Repository: repo, Command: []string{"/bin/sh", "-c",
		"echo check-started; while [ ! -f release ]; do sleep 0.05; done; echo after-disconnect; echo test-stderr >&2",
	}}
	task, err := first.Create(requestCtx, req, "same-check")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		events, err := first.Events(requestCtx, task.ID, 0)
		return err == nil && strings.Contains(joinEvents(events), "check-started\n")
	})
	disconnect()
	first.Close()

	second := NewClient(server.URL, tokenFile)
	defer second.Close()
	repeated, err := second.Create(ctx, req, "same-check")
	if err != nil || repeated.ID != task.ID || repeated.Status != tasks.Running {
		t.Fatalf("retry must find running task %s: %+v, %v", task.ID, repeated, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		items, err := second.List(ctx)
		return err == nil && len(items) == 1 && items[0].ID == task.ID && items[0].Status == tasks.Succeeded
	})
	events, err := second.Events(ctx, task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	log := joinEvents(events)
	if !strings.Contains(log, "after-disconnect\n") || !strings.Contains(log, "test-stderr\n") {
		t.Fatalf("output missing after reconnect: %q", log)
	}
	cursor := events[len(events)-1].Seq
	next, err := second.Events(ctx, task.ID, cursor)
	if err != nil || len(next) != 0 {
		t.Fatalf("resuming a finished log = %v, %v", next, err)
	}

	// A second job exercises cancellation while its command is still alive.
	blocked, err := second.Create(ctx, tasks.Request{Repository: repo, Command: []string{"/bin/sleep", "60"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		item, err := store.Get(ctx, blocked.ID)
		return err == nil && item.Status == tasks.Running
	})
	var conflict *HTTPError
	if _, err := second.Cancel(ctx, blocked.ID, true); !errors.As(err, &conflict) || conflict.Status != http.StatusConflict {
		t.Fatalf("removing a queued request after it starts = %v, want conflict", err)
	}
	if _, err := second.Cancel(ctx, blocked.ID, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		item, err := store.Get(ctx, blocked.ID)
		return err == nil && item.Status == tasks.Cancelled
	})
}

func TestSelectedSkillMustStillBeInstalledAtSend(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := startTestServer(t, dir, Handler(token, store, tasks.NewWorker(store, nil, nil), nil))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()

	if _, err := client.InstallSkill(ctx, "---\nname: triage\ndescription: Triage CI.\n---\nFind the first failing step."); err != nil {
		t.Fatal(err)
	}
	input := tasks.MessageRequest{Text: "/triage the run", SkillName: "triage"}
	first, err := client.SendMessage(ctx, tasks.DefaultConversationID, "original", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveSkill(ctx, input.SkillName); err != nil {
		t.Fatal(err)
	}
	if retry, err := client.SendMessage(ctx, tasks.DefaultConversationID, "original", input); err != nil || retry.ID != first.ID {
		t.Fatalf("retry should keep the accepted skill: %+v, %v", retry, err)
	}
	_, err = client.SendMessage(ctx, tasks.DefaultConversationID, "removed", input)
	var remote *HTTPError
	if !errors.As(err, &remote) || remote.Status != http.StatusBadRequest || !strings.Contains(remote.Message, "/triage is no longer installed") {
		t.Fatalf("expected a removed skill error, got %v", err)
	}
	chat, err := client.Conversation(ctx, tasks.DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 1 || chat.Messages[0].SkillName != "triage" {
		t.Fatalf("removed skill should leave the accepted invocation as the only message: %+v, %v", chat.Messages, err)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := Handler("test-token", store, tasks.NewWorker(store, tasks.RunRepositoryCheck, nil), nil)
	for _, body := range []string{`{"repository":"relative"}`, `{"repository":"/","timeout_seconds":-3}`,
		`{"repository":"/"} {}`, `{"kind":"send_email","repository":"/"}`} {
		r := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var reply errorBody
		if err := json.NewDecoder(w.Result().Body).Decode(&reply); err != nil || reply.Error == "" || w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code %d body %s", body, w.Code, w.Body.String())
		}
	}
}

func initRepo(t *testing.T, dir string) string {
	t.Helper()
	repo := filepath.Join(dir, "test repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "init", "--quiet")
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return repo
}

func joinEvents(events []tasks.Event) string {
	var buf strings.Builder
	for _, event := range events {
		buf.WriteString(event.Text)
	}
	return buf.String()
}

func waitFor(t *testing.T, ctx context.Context, f func() bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !f() {
		select {
		case <-ctx.Done():
			t.Fatalf("condition did not become true: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func testToken(t *testing.T, dir string) (string, error) {
	t.Helper()
	if _, err := localconfig.EnsureIdentity(dir); err != nil {
		t.Fatal(err)
	}
	return localconfig.EnsureToken(dir)
}

func startTestServer(t *testing.T, dir string, handler http.Handler) *httptest.Server {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := localconfig.EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	return server
}

func TestClientWithholdsTokenFromUnverifiedServer(t *testing.T) {
	dir := t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	expected := startTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("verified server did not get the bearer")
		}
		write(w, http.StatusOK, []tasks.Task{})
	}))
	defer expected.Close()
	tokenFile := filepath.Join(dir, localconfig.TokenName)
	legit := NewClient(expected.URL, tokenFile)
	defer legit.Close()
	if _, err := legit.List(t.Context()); err != nil {
		t.Fatal(err)
	}

	unexpected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unverified listener received an HTTP request")
		write(w, http.StatusOK, []tasks.Task{})
	})
	plain := httptest.NewServer(unexpected)
	defer plain.Close()
	tlsImpostor := httptest.NewTLSServer(unexpected)
	defer tlsImpostor.Close()
	for _, base := range []string{plain.URL, tlsImpostor.URL} {
		client := NewClient(base, tokenFile)
		if _, err := client.List(t.Context()); err == nil {
			t.Fatalf("accepted unverified server %s", base)
		}
		client.Close()
	}
}

func TestWorkerCredentialCannotReadOwnerRoutes(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler("owner-secret", store, tasks.NewWorker(store, nil, nil), nil)
	for _, route := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/v1/workspace", worker.Token, 401},
		{"GET", "/v1/tasks", worker.Token, 401},
		{"POST", "/v1/conversations/conv_buddy/watch-drafts/draft/activate", worker.Token, 401},
		{"POST", "/v1/conversations/conv_buddy/watch-drafts/draft/activate", "owner-secret", 404},
		{"POST", "/v1/worker/" + worker.ID + "/claim", "owner-secret", 401},
		{"POST", "/v1/worker/" + worker.ID + "/claim", worker.Token, 200},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		req.Header.Set("Authorization", "Bearer "+route.token)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != route.status {
			t.Errorf("%s %s returned %d, want %d", route.method, route.path, out.Code, route.status)
		}
	}
}
