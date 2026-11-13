package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"buddy/internal/localconfig"
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

func TestPairedPhoneCanQueueWorkAndCreateABud(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pair, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, pair.Code, "Work phone")
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.EnrollDevice(ctx, "Build Mac")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.SaveProject(ctx, tasks.Project{Name: "Release tools"})
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHostProfile(ctx, device.ID, tasks.HostProfile{
		OS: "darwin", Architecture: "arm64", Environment: "host", Slots: 1, CPUs: 1,
		Capabilities: []tasks.Capability{{ID: "browser", Installed: true, Enabled: true, Ready: true}},
		Projects:     []tasks.ProjectBinding{{ProjectID: project.ID, Path: checkout}},
	}); err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	t.Setenv("CODEX_HOME", fixture)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("BUDDY_OPENAI_API_KEY", "")
	if err := os.Chmod(fixture, 0700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(fixture, "codex")
	plan := fmt.Sprintf(`{"reply":"","handoffs":[{"device_id":%q,"project_id":%q,"requirements":["browser"],"title":"Private release summary","brief":"Inspect the release project in the browser.","read_only":true}],"bud_proposal":{"name":"Unexpected Bud","role":"Reviewer","instructions":"Review things"}}`, device.ID, project.ID)
	event, err := json.Marshal(object{"type": "item.completed", "item": object{"type": "agent_message", "text": plan}})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" + string(event) + "'\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := tasks.NewCodexRuntime(cli, "", localconfig.CodexKey{Dir: fixture})
	if err != nil {
		t.Fatal(err)
	}
	worker := tasks.NewWorker(store, nil, runtime.Run)
	handler := Handler("owner-fixture", store, worker, nil)
	call := func(path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+phone.Token)
		req.Header.Set("Idempotency-Key", key)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s: status %d, want %d (%s)", path, res.Code, want, res.Body)
		}
		return res
	}
	const chat = "/v1/conversations/conv_buddy"
	const assignment = `{"text":"Use the browser on Build Mac to inspect the Release tools project and prepare the private release summary.","background":true,"attachments":[{"name":"build.log","data":"Y2hlY2sgQmV0YQ=="}]}`
	call(chat+"/messages", "release-job", assignment, http.StatusAccepted)
	call(chat+"/messages", "release-job", assignment, http.StatusOK)
	call(chat+"/messages", "release-job", strings.ReplaceAll(assignment, `,"background":true`, ""), http.StatusConflict)
	run, stop := context.WithTimeout(ctx, 6*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(run) }()
	defer func() { stop(); <-done }()
	var work []tasks.WorkItem
	waitFor(t, run, func() bool {
		work, err = store.WorkOverview(ctx, tasks.DefaultConversationID)
		return err == nil && len(work) == 1
	})
	if work[0].Status != tasks.Queued || work[0].Device != "Build Mac" || work[0].ProjectID != project.ID ||
		len(work[0].Requirements) != 1 || work[0].Requirements[0] != "browser" || work[0].Title != "Private release summary" {
		t.Fatalf("assignment lost its routing or title: %+v", work[0])
	}
	job, err := store.ReserveExecutionWork(ctx, device.ID, false, false)
	if err != nil || job == nil || len(job.Attachments) != 1 || job.Attachments[0].Name != "build.log" || string(job.Attachments[0].Data) != "check Beta" {
		t.Fatalf("worker did not receive the phone attachment: %+v, %v", job, err)
	}
	other, err := store.CreateAgent(ctx, tasks.Agent{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	team, err := store.TeamContext(ctx, other.ConversationID, other.ID)
	if err != nil || len(team.Assignments) != 1 || team.Assignments[0].Title != "Background assignment" {
		t.Fatalf("another chat saw a private title: %+v, %v", team.Assignments, err)
	}
	workContext, err := store.WorkContext(ctx, other.ConversationID, tasks.DefaultAgentID)
	if err != nil || len(workContext) != 1 || workContext[0].Title != "Background assignment" {
		t.Fatalf("a Bud in another chat saw a private title: %+v, %v", workContext, err)
	}
	call("/v1/work/"+work[0].ID+"/cancel", "", "", http.StatusOK)
	if job, err := store.Get(ctx, work[0].ID); err != nil || job.Status != tasks.Cancelled {
		t.Fatalf("expected cancelled assignment, got %+v: %v", job, err)
	}

	profile, err := store.Agent(ctx, tasks.DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.SaveVoiceBud(ctx, tasks.DefaultConversationID, "call", profile.ID,
		"Create a Bud named Finch and have Finch review the release notes. Show me the draft first.",
		tasks.BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review the release notes.", CreateNow: true, Evidence: "Create a Bud named Finch"},
		tasks.Handoff{AgentID: "new_bud", DeviceID: device.ID, Title: "Review release notes", Brief: "Review the notes.", ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	conversation, err := store.Conversation(ctx, tasks.DefaultConversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(conversation.Messages, func(message tasks.Message) bool {
		return message.Origin == "background_request" && message.BudProposal != nil
	}) {
		t.Fatalf("planner emitted an inline action: %+v", conversation.Messages)
	}
	draft := conversation.Messages[len(conversation.Messages)-1]
	if draft.BudProposal == nil || draft.BudProposal.ConversationID != "" ||
		!slices.Equal(draft.BudProposal.PendingWork, []string{"Review release notes"}) {
		t.Fatalf("missing pending draft: %+v", draft)
	}
	if slices.ContainsFunc(conversation.Work, func(job tasks.WorkItem) bool { return job.Title == "Review release notes" }) {
		t.Fatal("voice assignment started before the draft was approved")
	}
	path := chat + "/bud-proposals/" + draft.TaskID
	call(path, "", "", http.StatusBadRequest)
	call(path+"/approval", "", "", http.StatusBadRequest)
	path += "/approval"
	const approveWork = `{"approval":"start_work"}`
	var first, second tasks.Agent
	if err := json.Unmarshal(call(path, "", approveWork, http.StatusOK).Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(call(path, "", approveWork, http.StatusOK).Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if first.Name != "Finch" || first.ConversationID == "" || first.ID != second.ID {
		t.Fatalf("unexpected Bud on retry: %+v, %+v", first, second)
	}
	call(path, "", `{"approval":"create_only"}`, http.StatusConflict)
	work, err = store.WorkOverview(ctx, tasks.DefaultConversationID)
	if err != nil || len(work) != 2 || !slices.ContainsFunc(work, func(job tasks.WorkItem) bool {
		return job.Title == "Review release notes" && job.AgentID == first.ID && job.Device == "Build Mac"
	}) {
		t.Fatalf("mobile approval did not queue the saved voice assignment exactly once: %+v, %v", work, err)
	}
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
