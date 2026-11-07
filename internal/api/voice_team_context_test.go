package api

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestCallRetriesBurstUpdatesBeyondQueueAndTeamBudgets(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, tasks.DefaultConversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	dial, peers := newTestVoiceProvider(t)
	client, err := dial(ctx, "test-voice-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	<-peers
	provider, err := dial(ctx, "test-voice-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.CloseNow()
	voice := <-peers
	room := newVoiceRoom(ctx, client, store, nil, chat, "test-voice-secret", dial)
	member := &roomMember{agent: chat.Agent, conn: provider}
	room.members, room.selected, room.listener = []*roomMember{member}, member, member
	room.requested = true // Hold the floor while work accumulates.
	jobs := map[string]bool{}
	for batch := range 2 {
		var drafts []tasks.Handoff
		for index := range 6 {
			drafts = append(drafts, tasks.Handoff{Title: fmt.Sprintf("Work %d/%d", batch, index), Brief: "Do this distinct assignment."})
		}
		items, err := store.QueueVoiceAssignments(ctx, chat.ID, chat.Agent.ID, "call", fmt.Sprint(batch), drafts)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range items {
			jobs[job.ID] = false
		}
	}
	if err := room.syncTeam(); err != nil {
		t.Fatal(err)
	}
	worker := tasks.NewWorker(store, nil, func(_ context.Context, turn tasks.Turn, _ tasks.Emit) (string, error) {
		if strings.HasSuffix(turn.Title, "/5") {
			return "Which source should I use?", tasks.ErrWorkNeedsInput
		}
		return "Finished " + turn.Title, nil
	})
	lifetime, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(lifetime) }()
	defer func() { stop(); <-done }()
	for {
		states, err := store.WorkStates(ctx, chat.ID, room.startedAt)
		if err != nil {
			t.Fatal(err)
		}
		ready := len(states) == len(jobs)
		for _, state := range states {
			ready = ready && !state.Status.Active()
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("work didn't settle")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	<-done
	peer, err := store.CreateAgent(ctx, tasks.Agent{Name: "Other worker"})
	if err != nil {
		t.Fatal(err)
	}
	for batch := range 5 {
		var drafts []tasks.Handoff
		for index := range 8 {
			drafts = append(drafts, tasks.Handoff{Title: fmt.Sprintf("Elsewhere %d/%d", batch, index), Brief: "An unrelated pending assignment."})
		}
		if _, err := store.QueueVoiceAssignments(ctx, peer.ConversationID, peer.ID, "elsewhere", fmt.Sprint(batch), drafts); err != nil {
			t.Fatal(err)
		}
	}
	if err := room.syncTeam(); err != nil {
		t.Fatal(err)
	}
	if len(room.updates) != 8 || !room.team.Truncated {
		t.Fatal("fixture didn't fill both budgets")
	}
	room.clearReplyRequest()
	for range len(jobs) {
		if err := room.syncTeam(); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case event := <-voice.received:
				if event["type"] != "response.create" {
					continue
				}
				instructions := event["response"].(map[string]any)["instructions"].(string)
				index := strings.LastIndex(instructions, "\n{\"job\":")
				var evidence struct {
					Job struct {
						ID string `json:"id"`
					}
					Question string `json:"question"`
					Result   string `json:"result"`
				}
				if index < 0 {
					t.Fatal("missing work evidence")
				}
				if err := json.Unmarshal([]byte(instructions[index+1:]), &evidence); err != nil {
					t.Fatal(err)
				}
				if seen, exists := jobs[evidence.Job.ID]; !exists || seen {
					t.Fatalf("duplicate or unexpected update: %+v", evidence)
				}
				if evidence.Question == "" && evidence.Result == "" {
					t.Fatal("a result or input request was lost")
				}
				jobs[evidence.Job.ID] = true
			case <-ctx.Done():
				t.Fatal("lost queued voice updates")
			}
			break
		}
		room.clearReplyRequest()
	}
	if err := room.syncTeam(); err != nil || room.requested || len(room.updates) != 0 {
		t.Fatalf("repeated a delivered notification: %v", err)
	}
}
