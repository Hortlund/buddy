package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"buddy/internal/tasks"
)

func TestJobReferenceResolvesItsGroupForDesktopAndPhone(t *testing.T) {
	ctx := t.Context()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	peer, err := store.CreateAgent(ctx, tasks.Agent{Name: "Reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.QueueVoiceAssignments(ctx, tasks.DefaultConversationID, tasks.DefaultAgentID, "call", "work", []tasks.Handoff{{Title: "Build service", Brief: "Build the requested service."}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.JoinVoiceGroup(ctx, tasks.DefaultConversationID, "call", "Team call", peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, pair.Code, "Reference phone")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler("owner-fixture", store, tasks.NewWorker(store, nil, nil), nil)
	for _, token := range []string{"owner-fixture", phone.Token} {
		request := httptest.NewRequest(http.MethodGet, "/v1/work/"+jobs[0].ID, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("reference resolution failed: %d %s", response.Code, response.Body)
		}
		var detail tasks.WorkDetails
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.ConversationID != group.ID || detail.ID != jobs[0].ID {
			t.Fatalf("resolved the wrong job destination: %+v", detail)
		}
	}
	device, err := store.EnrollDevice(ctx, "Worker")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/work/"+jobs[0].ID, nil)
	request.Header.Set("Authorization", "Bearer "+device.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("worker credential could browse owner references: %d", response.Code)
	}
}
