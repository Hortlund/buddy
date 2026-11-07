package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestRemoteExecutionControlPinsTLSAndRestrictsArtifacts(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	device, err := store.EnrollDevice(ctx, "Remote worker")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.QueueVoiceAssignments(ctx, tasks.DefaultConversationID, tasks.DefaultAgentID, "call", "remote", []tasks.Handoff{{ExecutionPlan: tasks.ExecutionPlan{Requirements: []string{"browser"}}, DeviceID: device.ID, Title: "Check website", Brief: "Check the website.", ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHostProfile(ctx, device.ID, tasks.HostProfile{OS: "linux", Architecture: "amd64", CPUs: 2, Environment: "host", Capabilities: []tasks.Capability{{ID: "browser", Installed: true, Enabled: true, Ready: true}}}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ReserveExecutionWork(ctx, device.ID, false, false)
	if err != nil || job == nil {
		t.Fatal("worker did not claim", err)
	}
	server := startTestServer(t, dir, Handler(token, store, tasks.NewWorker(store, nil, nil), nil))
	defer server.Close()
	certificate, err := localconfig.ConnectionCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewConnectionClient(localconfig.Connection{Server: server.URL, Certificate: certificate, Token: device.Token, DeviceID: device.ID})
	defer worker.Close()
	response, err := worker.ForwardWorkControl(ctx, job.ID, job.ControlToken, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"team_context","arguments":{}}}`))
	if err != nil || !strings.Contains(string(response), "chat_lead_bud_id") {
		t.Fatalf("scoped MCP did not cross pinned TLS: %s %v", response, err)
	}
	if _, err := worker.ForwardWorkControl(ctx, job.ID, device.Token, json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)); err == nil {
		t.Fatal("broad device credential was accepted as a job token")
	}
	if err := worker.UploadWorkArtifact(ctx, device.ID, job.ID, job.Lease, tasks.WorkArtifact{Name: "report.md", Data: []byte("# Report")}); err != nil {
		t.Fatal(err)
	}
	var screen bytes.Buffer
	_ = png.Encode(&screen, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if err := worker.UploadExecutionFrame(ctx, device.ID, job.ID, job.Lease, tasks.ExecutionFrame{Provider: "playwright", Data: screen.Bytes()}); err != nil {
		t.Fatal(err)
	}
	owner := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer owner.Close()
	detail, err := owner.ReferencedWorkDetails(ctx, jobs[0].ID)
	if err != nil || len(detail.Artifacts) != 1 {
		t.Fatal("artifact not listed", err)
	}
	if err := owner.PauseAutomation(ctx, job.ID, true); err != nil {
		t.Fatal(err)
	}
	if paused, err := worker.AutomationControl(ctx, device.ID, job.ID, job.Lease); err != nil || !paused {
		t.Fatal("worker missed human control", err)
	}
	frame, err := owner.ExecutionFrame(ctx, job.ID)
	if err != nil || !frame.Paused || len(frame.Data) == 0 {
		t.Fatal("frame or takeover state missing", err)
	}
	if _, err := worker.WorkArtifact(ctx, job.ID, detail.Artifacts[0].ID); err == nil {
		t.Fatal("worker could browse owner artifacts")
	}
	if err := worker.UploadWorkArtifact(ctx, device.ID, job.ID, "wrong-lease", tasks.WorkArtifact{Name: "wrong.txt", Data: []byte("wrong")}); err == nil {
		t.Fatal("wrong lease uploaded an artifact")
	}
	pair, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, pair.Code, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(token, store, tasks.NewWorker(store, nil, nil), nil)
	for _, path := range []string{"/v1/hosts", "/v1/projects", "/v1/work/" + job.ID + "/frame", "/v1/work/" + job.ID + "/artifacts/" + detail.Artifacts[0].ID} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+phone.Token)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("phone couldn't inspect %s: %d", path, out.Code)
		}
	}
	if err := store.RevokeDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ForwardWorkControl(ctx, job.ID, job.ControlToken, json.RawMessage(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)); err == nil {
		t.Fatal("revoked worker retained tools")
	}
}
