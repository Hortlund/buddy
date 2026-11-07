package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestMobileVoiceAccessAndRevocation(t *testing.T) {
	t.Setenv("BUDDY_VOICE_API_KEY", "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pair, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, pair.Code, "Voice phone")
	if err != nil {
		t.Fatal(err)
	}
	key := localconfig.VoiceKey{Dir: dir}
	if err := key.Save("test-voice-secret"); err != nil {
		t.Fatal(err)
	}
	worker := tasks.NewWorker(store, nil, nil)
	dial, peers := newTestVoiceProvider(t)
	owner := http.NewServeMux()
	var inUse atomic.Bool
	voiceRoutes(owner, store, worker, key, dial, &inUse)
	mobile := mobileRoutes(owner, store, "")
	fullHandler := Handler("owner-fixture", store, worker, &tasks.CodexRuntime{Key: localconfig.CodexKey{Dir: dir}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		id, err := store.MobileClientID(r.Context(), token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if _, pattern := mobile.Handler(r); pattern == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mobile.ServeHTTP(w, r.WithContext(tasks.WithMobileClient(r.Context(), id)))
	}))
	defer server.Close()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, voiceKeyPath, nil)
		req.Header.Set("Authorization", "Bearer "+phone.Token)
		res := httptest.NewRecorder()
		fullHandler.ServeHTTP(res, req)
		want := http.StatusForbidden
		if method == http.MethodGet {
			want = http.StatusOK
		}
		if res.Code != want {
			t.Fatalf("voice settings %s = %d", method, res.Code)
		}
		if strings.Contains(res.Body.String(), "test-voice-secret") {
			t.Fatal("phone received the server's voice credential")
		}
	}
	conn, _, err := websocket.Dial(ctx, server.URL+voiceCallPath+"?chat="+tasks.DefaultConversationID, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + phone.Token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var ready VoiceEvent
	for ready.Type != "ready" {
		if err := wsjson.Read(ctx, conn, &ready); err != nil {
			t.Fatal(err)
		}
	}
	if len(ready.Participants) != 1 {
		t.Fatalf("phone roster = %+v", ready)
	}
	peer := <-peers
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 0, 2, 0}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-peer.received:
			if event["type"] == "input_audio_buffer.append" {
				goto relayed
			}
		case <-ctx.Done():
			t.Fatal("phone audio was not relayed")
		}
	}
relayed:
	if err := store.RevokeMobileClient(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	for {
		_, _, err := conn.Read(deadline)
		if err != nil {
			if deadline.Err() != nil {
				t.Fatal("revoked phone kept its voice socket")
			}
			break
		}
	}
	for inUse.Load() {
		select {
		case <-deadline.Done():
			t.Fatal("revoked call did not release the voice room")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, _, err := websocket.Dial(ctx, server.URL+voiceCallPath+"?chat="+tasks.DefaultConversationID, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + phone.Token}},
	}); err == nil {
		t.Fatal("revoked phone could reconnect")
	}
}
