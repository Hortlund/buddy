package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buddy/internal/tasks"
)

func TestCallRefreshesTeamLeadershipAndAnnouncesWorkOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	peer, err := store.CreateAgent(ctx, tasks.Agent{Name: "Orbit", Role: "Architect"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, tasks.GroupRequest{Name: "Workshop", Members: []string{tasks.DefaultAgentID, peer.ID}})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, group.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	dial, peers := newTestVoiceProvider(t)
	client, err := dial(ctx, "test-voice-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	<-peers // The client receives roster and work notifications.
	provider, err := dial(ctx, "test-voice-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.CloseNow()
	voice := <-peers
	room := newVoiceRoom(ctx, client, store, nil, chat, "test-voice-secret", dial)
	member := &roomMember{agent: chat.Agent, conn: provider, instructions: "Use current server team context."}
	room.members, room.selected, room.listener = []*roomMember{member}, member, member
	room.toolSteps = 4
	response := func() string {
		t.Helper()
		for {
			select {
			case event := <-voice.received:
				if event["type"] == "response.create" {
					return event["response"].(map[string]any)["instructions"].(string)
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for the Bud's speaking context")
				return ""
			}
		}
	}
	if err := store.SetGroupLead(ctx, group.ID, peer.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPrimaryAgent(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	if err := room.requestReply(voiceCaller, "Who is leading?"); err != nil {
		t.Fatal(err)
	}
	first := response()
	if !strings.Contains(first, `"primary_bud_id":"`+peer.ID+`"`) || !strings.Contains(first, `"chat_lead_bud_id":"`+peer.ID+`"`) {
		t.Fatalf("the call didn't get the current leader: %s", first)
	}
	room.clearReplyRequest()
	if err := store.RemoveAgent(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	if err := room.requestReply(voiceCaller, "Who is still on the team?"); err != nil {
		t.Fatal(err)
	}
	second := response()
	if room.leadID != tasks.DefaultAgentID || len(room.directory) != 1 || !strings.Contains(second, `"kind":"left_team"`) {
		t.Fatalf("the call kept a removed teammate or stale leader: %s", second)
	}
	room.clearReplyRequest()
	jobs, err := store.QueueVoiceAssignments(ctx, group.ID, tasks.DefaultAgentID, "call", "work", []tasks.Handoff{{Title: "Build the server", Brief: "Build the requested service."}})
	if err != nil {
		t.Fatal(err)
	}
	if err := room.syncTeam(); err != nil {
		t.Fatal(err)
	}
	worker := tasks.NewWorker(store, nil, func(context.Context, tasks.Turn, tasks.Emit) (string, error) {
		return "Built the service and checked its tests.", nil
	})
	lifetime, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(lifetime) }()
	defer func() { stop(); <-done }()
	for {
		detail, err := store.WorkDetails(ctx, group.ID, jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Status == tasks.Succeeded {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("work didn't finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := room.syncTeam(); err != nil {
		t.Fatal(err)
	}
	if update := response(); !strings.Contains(update, "Built the service and checked its tests.") {
		t.Fatalf("work completion wasn't delivered to the call: %s", update)
	}
	room.clearReplyRequest()
	if err := room.syncTeam(); err != nil || room.requested || len(room.updates) != 0 {
		t.Fatalf("a completed job was announced twice: %v", err)
	}
}
