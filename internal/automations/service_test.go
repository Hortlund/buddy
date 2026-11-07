package automations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestWatchNotificationAndDatabaseSnapshot(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	watch, err := store.CreateWatch(ctx, tasks.Watch{Name: "Builds", Scope: "org/repo", Source: tasks.GitHubCI,
		ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchAsk, Budget: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordStandingSignal(ctx, tasks.StandingSignal{Source: watch.Source, Scope: watch.Scope,
		Key: "42:1", Title: "Build failed", Detail: "Private provider detail"}); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan map[string]string, 2)
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.Header.Get("Authorization") != "Bearer relay-fixture" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body["key"] = req.Header.Get("Idempotency-Key")
		delivered <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer relay.Close()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "snapshots")
	service := New(store, localconfig.StandingConfig{NotifyURL: relay.URL, NotifyBearer: "relay-fixture", SnapshotDir: dir, SnapshotKeep: 1})
	service.client = relay.Client()
	service.notify(ctx)
	service.notify(ctx)
	if len(delivered) != 1 {
		t.Fatalf("expected one relay request, got %d", len(delivered))
	}
	if body := <-delivered; len(body) != 5 || body["kind"] != "approval" || body["key"] != body["id"]+":approval" {
		t.Fatalf("unexpected relay body: %+v", body)
	}
	service.snapshot(ctx)
	p, err := store.StandingPoll(ctx, "snapshot", "database")
	if err != nil || p.Problem == "" {
		t.Fatalf("missing snapshot destination was not reported: %+v, %v", p, err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("snapshot failure made Buddy unavailable: %v", err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"buddy-20000101T000000Z-1.db", "owner-copy.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.writeSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	service.snapshot(ctx)
	if _, err := os.Stat(filepath.Join(dir, "owner-copy.db")); err != nil {
		t.Fatalf("retention changed an unrelated file: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "buddy-*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("snapshot files: %v, %v", files, err)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot permissions: %v, %v", info, err)
	}
	restored, err := tasks.OpenStore(ctx, files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	inbox, err := restored.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].State != "approval" {
		t.Fatalf("snapshot inbox: %+v, %v", inbox, err)
	}
}
