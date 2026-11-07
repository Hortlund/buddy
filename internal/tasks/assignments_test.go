package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTeamAssignmentsRunAlongsideChatAndShareResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	forge, err := store.CreateAgent(ctx, Agent{Name: "Forge", Role: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Workshop", Members: []string{DefaultAgentID, forge.ID}})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan Turn, 3)
	release := make(chan struct{})
	worker := NewWorker(store, nil, func(ctx context.Context, turn Turn, _ Emit) (string, error) {
		if turn.Origin == "handoff" {
			started <- turn
			if turn.Title == "Combine findings" {
				return "Combined both results.", nil
			}
			select {
			case <-release:
				return turn.Agent.Name + " found an answer.", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if turn.Messages[len(turn.Messages)-1].Text == "Split the work." {
			turn.action.Handoffs = []Handoff{
				{Title: "Check the design", Brief: "Inspect the design."},
				{AgentID: forge.ID, Title: "Check the build", Brief: "Inspect the build."},
				{Title: "Combine findings", Brief: "Compare both results.", After: []int{1, 2}},
			}
			return "", nil
		}
		if turn.WorkDetails != nil && turn.Agent.ID == forge.ID && turn.WorkDetails.Status == Running {
			return "My build check is running.", nil
		}
		return "I don't have the requested job context.", nil
	})
	lifetime, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(lifetime) }()
	defer func() { stop(); <-done }()
	if _, _, err := store.Send(ctx, group.ID, "Split the work.", "assign"); err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	next := func() Turn {
		t.Helper()
		select {
		case turn := <-started:
			return turn
		case <-ctx.Done():
			t.Fatal("timed out waiting for an assignment")
			return Turn{}
		}
	}
	first, second := next(), next()
	if first.Agent.ID == second.Agent.ID || first.Title == "Combine findings" || second.Title == "Combine findings" {
		t.Fatalf("expected distinct independent jobs to start first: %s and %s", first.Title, second.Title)
	}
	items, err := store.WorkOverview(ctx, group.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("jobs were not saved together: %+v, %v", items, err)
	}
	var buildID string
	for _, item := range items {
		if item.AgentID == forge.ID && item.Status == Running {
			buildID = item.ID
		}
		if item.Title == "Combine findings" && item.Status != Queued {
			t.Fatal("dependent work ran while its prerequisites were still active")
		}
	}
	if buildID == "" {
		t.Fatal("Forge's job was not running")
	}
	question, _, err := store.SendWithWork(ctx, group.ID, "", "How is the build?", "question", nil, buildID)
	if err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	waitForConversation(t, store, group.ID, func(chat Conversation) bool {
		return slices.ContainsFunc(chat.Messages, func(message Message) bool {
			return message.TaskID == question.ID && message.AgentID == forge.ID && message.Text == "My build check is running."
		})
	})
	close(release)
	last := next()
	if last.Title != "Combine findings" || !strings.Contains(last.Brief, "Buddy found an answer.") ||
		!strings.Contains(last.Brief, "Forge found an answer.") {
		t.Fatalf("dependent job did not receive the completed results: %s", last.Brief)
	}
	waitForConversation(t, store, group.ID, func(chat Conversation) bool {
		return slices.ContainsFunc(chat.Messages, func(message Message) bool { return message.Text == "Combined both results." })
	})
}

func TestPrivateAssignmentsShareOnlyScopedContextAndRollBackTogether(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	peer, err := store.CreateAgent(ctx, Agent{Name: "Scout"})
	if err != nil {
		t.Fatal(err)
	}
	parent, _, err := store.Send(ctx, DefaultConversationID, "Private context and assignment.", "private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	action := &TurnAction{Handoffs: []Handoff{{AgentID: peer.ID, Title: "Review", Brief: "Review the metrics."}}}
	if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", "", action); err != nil {
		t.Fatal(err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, job)
	if err != nil || turn.Agent.ID != peer.ID || len(turn.Messages) != 0 || turn.Brief != "Review the metrics." {
		t.Fatalf("outside assignment received the wrong context: %+v, %v", turn, err)
	}
	status, err := store.WorkContext(ctx, peer.ConversationID, peer.ID)
	if err != nil || len(status) != 1 || status[0].ID != job.ID || status[0].Activity != "" {
		t.Fatalf("the Bud did not receive its cross-chat job status: %+v, %v", status, err)
	}
	invalid, _, err := store.Send(ctx, DefaultConversationID, "Two more assignments.", "invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	action.Handoffs = append(action.Handoffs, Handoff{Title: "Invalid", Brief: "Work.", After: []int{2}})
	if err := store.finishTurn(ctx, invalid.ID, Succeeded, "Replied.", "", action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an invalid batch left part of the work queued: %v", err)
	}
}

func TestAssignmentsUpgradePreservesWorkAndWaitsAcrossDevices(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	parent, _, err := store.Send(ctx, DefaultConversationID, "Inspect the report.", "old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", "", &TurnAction{
		Handoff: &Handoff{Title: "Existing handoff", Brief: "Read the report."},
	}); err != nil {
		t.Fatal(err)
	}
	// Recreate the earlier schema around a real chat and its existing job.
	oldTable := strings.Replace(turnDetailsTable, "parent_id TEXT REFERENCES", "parent_id TEXT UNIQUE REFERENCES", 1)
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE old_details `+oldTable+`;
		INSERT INTO old_details SELECT * FROM turn_details;
		DROP TABLE turn_details;
		ALTER TABLE old_details RENAME TO turn_details;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store = upgraded
	old, err := store.WorkOverview(ctx, DefaultConversationID)
	if err != nil || len(old) != 1 || old[0].Title != "Existing handoff" {
		t.Fatalf("lost existing handoff during upgrade: %+v, %v", old, err)
	}
	studio, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	laptop, err := store.EnrollDevice(ctx, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := store.Send(ctx, DefaultConversationID, "Check on Studio, then summarize on Laptop.", "new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, request.ID, Succeeded, "Replied.", "", &TurnAction{Handoffs: []Handoff{
		{DeviceID: studio.ID, Title: "Check report", Brief: "Check the report."},
		{DeviceID: laptop.ID, Title: "Summarize report", Brief: "Summarize the check.", After: []int{1}},
	}}); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimDeviceJob(ctx, studio.ID)
	if err != nil || first == nil || first.Title != "Check report" {
		t.Fatalf("Studio did not receive its assignment: %+v, %v", first, err)
	}
	if job, err := store.ClaimDeviceJob(ctx, laptop.ID); err != nil || job != nil {
		t.Fatalf("Laptop started before its prerequisite finished: %+v, %v", job, err)
	}
	if err := store.FinishDeviceJob(ctx, studio.ID, first.ID, DeviceResult{
		Lease: first.Lease, Status: Succeeded, Reply: "Studio found two errors.",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimDeviceJob(ctx, laptop.ID)
	if err != nil || second == nil || second.Title != "Summarize report" || !strings.Contains(second.Brief, "Studio found two errors.") {
		t.Fatalf("Laptop did not receive the completed result: %+v, %v", second, err)
	}
}
