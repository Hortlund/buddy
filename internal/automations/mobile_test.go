package automations

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
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

func TestMobilePushDeliverySurvivesRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	store, err := tasks.OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	invite, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, invite.Code, "Fixture iPhone")
	if err != nil {
		t.Fatal(err)
	}
	const topic = "net.example.my-buddy"
	token := strings.Repeat("af", 32)
	if err := store.SetMobileNotifications(ctx, phone.ID, token, false); err != nil {
		t.Fatal(err)
	}
	watch, err := store.CreateWatch(ctx, tasks.Watch{Name: "Private CI", Source: tasks.GitHubCI,
		Scope: "org/private", ConversationID: tasks.DefaultConversationID, Mode: tasks.WatchNotify, Budget: 5})
	if err != nil {
		t.Fatal(err)
	}
	signal := tasks.StandingSignal{Source: watch.Source, Scope: watch.Scope, Key: "42:1",
		Title: "Private failure", Detail: "Private evidence"}
	if ok, err := store.RecordStandingSignal(ctx, signal); !ok || err != nil {
		t.Fatalf("record: %v, %v", ok, err)
	}
	if ok, err := store.RecordStandingSignal(ctx, signal); ok || err != nil {
		t.Fatalf("duplicate: %v, %v", ok, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan []byte, 2)
	apple := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/3/device/"+token || req.Header.Get("apns-topic") != topic ||
			!strings.HasPrefix(req.Header.Get("Authorization"), "bearer ") || req.Header.Get("apns-push-type") != "alert" ||
			req.Header.Get("apns-collapse-id") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(req.Body)
		delivered <- body
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer apple.Close()
	service := New(store, localconfig.StandingConfig{})
	service.client = apple.Client()
	service.mobile = &applePush{origin: apple.URL, team: "fixture-team", keyID: "fixture-key", topic: topic, key: key}
	service.notifyMobile(ctx)
	if len(delivered) != 1 {
		t.Fatalf("expected one notification, got %d", len(delivered))
	}
	body := <-delivered
	if strings.Contains(string(body), "Private") || strings.Contains(string(body), "org/") {
		t.Fatalf("APNs payload leaked watch evidence: %s", body)
	}
	var notice struct {
		Buddy map[string]string `json:"buddy"`
	}
	if err := json.Unmarshal(body, &notice); err != nil || notice.Buddy["destination"] != "inbox" ||
		notice.Buddy["client"] != phone.ID || notice.Buddy["item"] == "" {
		t.Fatalf("notification missing its route: %s", body)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = tasks.OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.NextMobileDelivery(ctx, time.Now().Add(2*time.Minute))
	if err != nil || retry.Attempt != 1 || retry.Item != notice.Buddy["item"] {
		t.Fatalf("pending delivery lost on restart: %+v, %v", retry, err)
	}
	if err := store.RevokeMobileClient(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextMobileDelivery(context.Background(), time.Now().Add(2*time.Minute)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked iPhone kept its notification: %v", err)
	}
}
