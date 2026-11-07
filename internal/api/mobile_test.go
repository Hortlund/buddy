package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"buddy/internal/tasks"
)

func TestPhonePairingAndWatchHandoff(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := tasks.NewWorker(store, nil, func(_ context.Context, turn tasks.Turn, _ tasks.Emit) (string, error) {
		if turn.WatchSignal == nil {
			return "Hello from Buddy", nil
		}
		if strings.Contains(turn.Brief, "Linux CI runner") {
			return "The Linux runner failed its tests.", nil
		}
		return "Which runner should I investigate?", tasks.ErrWorkNeedsInput
	})
	handler := Handler("owner-fixture", store, worker, nil)
	call := func(method, path, token string, input any, want int, out any) {
		t.Helper()
		var body string
		if input != nil {
			b, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			body = string(b)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}

	var old, pair tasks.MobilePairing
	call(http.MethodPost, "/v1/mobile/pairings", "", nil, http.StatusUnauthorized, nil)
	call(http.MethodPost, "/v1/mobile/pairings", "owner-fixture", nil, http.StatusCreated, &old)
	call(http.MethodPost, "/v1/mobile/pairings", "owner-fixture", nil, http.StatusCreated, &pair)
	call(http.MethodPost, mobileRedeem, "", map[string]string{"code": old.Code, "name": "Phone"}, http.StatusGone, nil)
	input := map[string]string{"code": pair.Code, "name": "Fixture iPhone"}
	var phone tasks.MobileCredential
	call(http.MethodPost, mobileRedeem, "", input, http.StatusCreated, &phone)
	call(http.MethodPost, mobileRedeem, "", input, http.StatusGone, nil)
	for _, path := range []string{"/v1/mobile/clients", "/v1/watches", "/v1/runtime/codex/mcp", "/v1/tasks"} {
		call(http.MethodGet, path, phone.Token, nil, http.StatusForbidden, nil)
	}
	call(http.MethodGet, "/v1/workspace", phone.Token, nil, http.StatusOK, nil)
	call(http.MethodPost, "/v1/conversations/conv_buddy/messages", phone.Token, tasks.MessageRequest{Text: "Hello from iPhone"}, http.StatusAccepted, nil)

	watch, err := store.CreateWatch(ctx, tasks.Watch{
		ConversationID: tasks.DefaultConversationID, Name: "CI", Source: tasks.GitHubCI, Scope: "acme/buddy",
		Mode: tasks.WatchAsk, Budget: 5, Instructions: "Investigate CI.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.RecordStandingSignal(ctx, tasks.StandingSignal{
		Source: watch.Source, Scope: watch.Scope, Key: "42:1", Title: "CI failed",
	}); !ok || err != nil {
		t.Fatalf("record watch: %v, %v", ok, err)
	}
	var inbox []tasks.InboxItem
	call(http.MethodGet, "/v1/inbox", phone.Token, nil, http.StatusOK, &inbox)
	if len(inbox) != 1 || inbox[0].State != "approval" {
		t.Fatalf("missing watch approval: %+v", inbox)
	}
	item := "/v1/inbox/" + inbox[0].ID
	call(http.MethodGet, item, phone.Token, nil, http.StatusOK, nil)
	call(http.MethodPatch, item, phone.Token, map[string]string{"decision": "approve"}, http.StatusOK, nil)
	call(http.MethodPatch, item, phone.Token, map[string]string{"decision": "approve"}, http.StatusConflict, nil)

	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(run)
	}()
	defer func() { cancel(); <-done }()

	waitWork := func(status tasks.Status) tasks.WorkItem {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			var work []tasks.WorkItem
			call(http.MethodGet, "/v1/work", phone.Token, nil, http.StatusOK, &work)
			if len(work) == 1 && work[0].Status == status {
				return work[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("work never reached %s", status)
		return tasks.WorkItem{}
	}
	job := waitWork(tasks.NeedsInput)
	call(http.MethodPost, "/v1/conversations/conv_buddy/work-inputs/"+job.InputID+"/answer",
		phone.Token, map[string]string{"text": "Linux CI runner"}, http.StatusOK, nil)
	if completed := waitWork(tasks.Succeeded); completed.ID != job.ID {
		t.Fatalf("answer created a different job: %s", completed.ID)
	}
	var detail tasks.WorkDetails
	call(http.MethodGet, "/v1/conversations/conv_buddy/work/"+job.ID, phone.Token, nil, http.StatusOK, &detail)
	if detail.Result != "The Linux runner failed its tests." {
		t.Fatalf("unexpected job result: %q", detail.Result)
	}
	var desktop tasks.Conversation
	call(http.MethodGet, "/v1/conversations/conv_buddy", "owner-fixture", nil, http.StatusOK, &desktop)
	var sent, answered bool
	for _, message := range desktop.Messages {
		sent = sent || message.Text == "Hello from iPhone"
		answered = answered || message.Text == detail.Result
	}
	if !sent || !answered {
		t.Fatalf("desktop didn't see mobile work in the same chat: sent=%v, finished=%v", sent, answered)
	}

	call(http.MethodDelete, "/v1/mobile/clients/"+phone.ID, phone.Token, nil, http.StatusForbidden, nil)
	call(http.MethodDelete, "/v1/mobile/clients/"+phone.ID, "owner-fixture", nil, http.StatusNoContent, nil)
	call(http.MethodGet, "/v1/workspace", phone.Token, nil, http.StatusUnauthorized, nil)
}

type pausedMobileBody struct {
	io.Reader
	started chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (b *pausedMobileBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.resume
	})
	return b.Reader.Read(p)
}

func TestRevokedPhoneCannotFinishPendingMessageUpload(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	invite, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, invite.Code, "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler("owner-fixture", store, tasks.NewWorker(store, nil, nil), nil)
	body := &pausedMobileBody{
		Reader:  strings.NewReader(`{"text":"Late message"}`),
		started: make(chan struct{}), resume: make(chan struct{}),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/conv_buddy/messages", body)
	req.Header.Set("Authorization", "Bearer "+phone.Token)
	out := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(out, req)
	}()
	revoke := httptest.NewRecorder()
	func() {
		defer func() { close(body.resume); <-done }()
		select {
		case <-body.started:
		case <-time.After(2 * time.Second):
			t.Fatal("upload didn't reach its body")
		}
		req := httptest.NewRequest(http.MethodDelete, "/v1/mobile/clients/"+phone.ID, nil)
		req.Header.Set("Authorization", "Bearer owner-fixture")
		handler.ServeHTTP(revoke, req)
	}()
	if revoke.Code != http.StatusNoContent || out.Code != http.StatusUnauthorized {
		t.Fatalf("revoke status=%d, late write status=%d", revoke.Code, out.Code)
	}
	chat, err := store.Conversation(ctx, tasks.DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 0 {
		t.Fatalf("late write changed the conversation: %v, %v", chat.Messages, err)
	}
}
