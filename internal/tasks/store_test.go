package tasks

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRecoveryKeepsQueueAndLogs(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()

	req := Request{Kind: RepoCheck, Repository: t.TempDir(), Command: []string{"go", "test", "./..."}, TimeoutSeconds: 600}
	running, _, err := store.Create(ctx, req, "running")
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.Create(ctx, req, "queued")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claim(ctx)
	if err != nil || claimed.ID != running.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := store.append(ctx, running.ID, "stdout", "saved before restart\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(ctx, running.ID)
	if err != nil || task.Status != Interrupted || task.FinishedAt == nil {
		t.Fatalf("recovered in-flight task = %+v, %v", task, err)
	}
	events, err := store.Events(ctx, running.ID, 0)
	if err != nil || len(events) != 4 || events[2].Text != "saved before restart\n" ||
		!strings.Contains(events[3].Text, "server stopped") {
		t.Fatalf("recovered events = %+v, %v", events, err)
	}
	claimed, err = store.claim(ctx)
	if err != nil || claimed.ID != queued.ID || claimed.Status != Running {
		t.Fatalf("claim after recovery = %+v, %v", claimed, err)
	}
}

func TestConversationPagesAndExactWorkspace(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "project ")
	for _, path := range []string{workspace, strings.TrimSpace(workspace)} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := store.SaveAgent(ctx, DefaultAgentID, Agent{Name: "Buddy", Workspace: workspace, AllowEdits: true})
	canonical, resolveErr := filepath.EvalSymlinks(workspace)
	if err != nil || resolveErr != nil || saved.Workspace != canonical {
		t.Fatalf("workspace changed: %q, %v, %v", saved.Workspace, err, resolveErr)
	}
	for i := range 62 {
		text := fmt.Sprintf("message %d", i)
		task, _, err := store.Send(ctx, DefaultConversationID, text, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.claim(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.finish(ctx, task.ID, Succeeded, "Replied.", text); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(latest.Messages) != 120 || latest.Before != latest.Messages[0].ID {
		t.Fatalf("latest page has %d messages, cursor %d, error %v", len(latest.Messages), latest.Before, err)
	}
	older, err := store.Conversation(ctx, DefaultConversationID, latest.Before)
	if err != nil || len(older.Messages) != 4 || older.Before != 0 ||
		older.Messages[0].Text != "message 0" || older.Messages[3].ID+1 != latest.Messages[0].ID {
		t.Fatalf("earlier page: %+v, %v", older, err)
	}
}

func TestIdempotencySeparatesKindsAndKeepsLegacyKeys(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	req := Request{Kind: RepoCheck, Repository: t.TempDir(), TimeoutSeconds: 60}
	repo, _, err := store.Create(ctx, req, "chat:shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE tasks SET request_key=? WHERE id=?", "chat:shared", repo.ID); err != nil {
		t.Fatal(err)
	}
	chat, created, err := store.Send(ctx, DefaultConversationID, "hello", "shared")
	if err != nil || !created || chat.ID == repo.ID {
		t.Fatalf("chat collided with a legacy check: %+v, %v", chat, err)
	}
	retry, created, err := store.Create(ctx, req, "chat:shared")
	if err != nil || created || retry.ID != repo.ID {
		t.Fatalf("legacy check retry = %+v, %v, %v", retry, created, err)
	}
	retry, created, err = store.Send(ctx, DefaultConversationID, "hello", "shared")
	if err != nil || created || retry.ID != chat.ID {
		t.Fatalf("chat retry = %+v, %v, %v", retry, created, err)
	}

	legacy, _, err := store.Send(ctx, DefaultConversationID, "older chat", "old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE tasks SET request_key=? WHERE id=?", "chat:old", legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.Create(ctx, req, "chat:old"); err != nil || !created {
		t.Fatalf("check collided with a legacy chat: %v, %v", created, err)
	}
	retry, created, err = store.Send(ctx, DefaultConversationID, "older chat", "old")
	if err != nil || created || retry.ID != legacy.ID {
		t.Fatalf("legacy chat retry = %+v, %v, %v", retry, created, err)
	}
}

func TestAvatarPersistsWithLegacyProfileUpdates(t *testing.T) {
	store, err := OpenStore(t.Context(), filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var picture bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if err := png.Encode(&picture, img); err != nil {
		t.Fatal(err)
	}
	want := Avatar{Color: "peach", Shape: "bean", Image: picture.Bytes()}
	if _, err := store.SaveAgent(t.Context(), DefaultAgentID, Agent{Name: "Scout", Avatar: &want}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAgent(t.Context(), DefaultAgentID, Agent{Name: "Scout renamed"}); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(t.Context(), DefaultConversationID, 0)
	if err != nil || chat.Agent.Avatar == nil {
		t.Fatalf("avatar missing: %v", err)
	}
	got := chat.Agent.Avatar
	if got.Color != want.Color || got.Shape != want.Shape || got.Animate != want.Animate || !bytes.Equal(got.Image, want.Image) {
		t.Fatal("legacy profile update replaced the avatar")
	}
	got.Image = []byte("not a picture")
	if _, err := store.SaveAgent(t.Context(), DefaultAgentID, Agent{Name: "Scout", Avatar: got}); err == nil {
		t.Fatal("invalid profile picture was accepted")
	}
}

func TestAvatarShapeUpgradePreservesExistingProfile(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.ExecContext(ctx, schema+chatSchema+`
		INSERT INTO agent_appearance(agent_id,color,face,finish,animate) VALUES ('agt_buddy','sky','sleepy','matte',0);`)
	old.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil || agent.Avatar == nil || agent.Avatar.Shape != "orb" || agent.Avatar.Color != "sky" || agent.Avatar.Animate {
		t.Fatalf("upgraded profile = %+v, %v", agent.Avatar, err)
	}
	agent.Avatar.Shape = "pebble"
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	agent.Avatar.Shape = "" // Older clients omit this field.
	saved, err := store.SaveAgent(ctx, agent.ID, agent)
	if err != nil || saved.Avatar == nil || saved.Avatar.Shape != "pebble" {
		t.Fatalf("shape after legacy save = %+v, %v", saved.Avatar, err)
	}
}

func TestLatestConversationRefreshesEarlierSteering(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const conversation = DefaultConversationID
	if _, _, err := store.Send(ctx, conversation, "long-running request", ""); err != nil {
		t.Fatal(err)
	}
	active, err := store.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 60 {
		queued, _, err := store.Send(ctx, conversation, "queued follow-up", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.cancel(ctx, queued.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.queueSteering(ctx, active.ID, "adjust the active request", "", true); err != nil {
		t.Fatal(err)
	}
	check := func(wantStatus Status, wantSteering string) {
		t.Helper()
		page, err := store.Conversation(ctx, conversation, 0)
		if err != nil || len(page.Messages) != 120 || page.Before != page.Messages[0].ID || len(page.EarlierUpdates) == 0 {
			t.Fatalf("page did not include the earlier turn update: %d messages, %d updates, %v", len(page.Messages), len(page.EarlierUpdates), err)
		}
		m := page.EarlierUpdates[0]
		if m.TaskID != active.ID || m.Status != wantStatus || len(m.Steering) != 1 || m.Steering[0].State != wantSteering {
			t.Fatalf("earlier update = %+v, want %s and %s", m, wantStatus, wantSteering)
		}
	}
	check(Running, steerPending)
	if err := store.finish(ctx, active.ID, Succeeded, "Replied.", "Done."); err != nil {
		t.Fatal(err)
	}
	check(Succeeded, steerMissed)
}

func TestLatestConversationRefreshesEarlierBudApproval(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	const chat = DefaultConversationID
	draft := BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review logs."}
	if _, _, _, err := s.SaveVoiceBud(ctx, chat, "draft-call", DefaultAgentID, "Draft a Bud named Finch.", draft,
		Handoff{AgentID: newBudAssignee, Title: "Review logs", Brief: "Review the logs."}); err != nil {
		t.Fatal(err)
	}
	buddy, err := s.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	for range 120 {
		if err := s.RecordVoiceLine(ctx, chat, "newer-call", buddy, "user", "Newer caption."); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := s.Conversation(ctx, chat, 0)
	if err != nil || latest.Before == 0 {
		t.Fatalf("expected paginated chat: %d, %v", latest.Before, err)
	}
	older, err := s.Conversation(ctx, chat, latest.Before)
	if err != nil || len(older.Messages) != 1 || older.Messages[0].BudProposal == nil || older.Messages[0].BudProposal.ConversationID != "" {
		t.Fatalf("expected older draft: %+v, %v", older.Messages, err)
	}
	bud, err := s.CreateProposedBud(ctx, chat, older.Messages[0].TaskID, CreateBudOnly)
	if err != nil {
		t.Fatal(err)
	}
	update, err := s.ConversationWithReactions(ctx, chat, 0, latest.ReactionRevision)
	if err != nil || len(update.EarlierUpdates) != 1 {
		t.Fatalf("approval did not reach polling client: %+v, %v", update.EarlierUpdates, err)
	}
	message := update.EarlierUpdates[0]
	if message.ID != older.Messages[0].ID || message.BudProposal == nil || message.BudProposal.ConversationID != bud.ConversationID {
		t.Fatalf("approval update did not link the Bud: %+v", message)
	}
	next, err := s.ConversationWithReactions(ctx, chat, 0, update.ReactionRevision)
	if err != nil || len(next.EarlierUpdates) != 0 {
		t.Fatalf("approval update was repeated after the cursor advanced: %+v, %v", next.EarlierUpdates, err)
	}
}

func TestUndeliveredSteeringDoesNotDisplaceTurnContext(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const conversation = DefaultConversationID
	if _, _, err := store.Send(ctx, conversation, strings.Repeat("a", 12000), ""); err != nil {
		t.Fatal(err)
	}
	active, err := store.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	applied, _, err := store.queueSteering(ctx, active.ID, strings.Repeat("b", 12000), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.markSteering(applied.ID, steerApplied); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.queueSteering(ctx, active.ID, strings.Repeat("c", 12000), "", true); err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, active.ID, Succeeded, "Replied.", strings.Repeat("d", 32000)); err != nil {
		t.Fatal(err)
	}
	followUp, _, err := store.Send(ctx, conversation, strings.Repeat("e", 6000), "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, followUp)
	if err != nil || len(turn.Messages) != 3 || turn.Messages[0].TaskID != active.ID || turn.Messages[0].Role != "user" {
		t.Fatalf("original request should fit with the applied correction, got %d messages, %v", len(turn.Messages), err)
	}
}

func TestAgentModelDefaultsAndQueuedTurnKeepsItsSelection(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	profile, err := store.Agent(ctx, DefaultAgentID)
	if err != nil || profile.Model != DefaultCodexModel {
		t.Fatalf("default model = %q, %v", profile.Model, err)
	}
	profile.Model = "gpt-6.1-sol"
	if _, err := store.SaveAgent(ctx, profile.ID, profile); err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.Send(ctx, DefaultConversationID, "check the project", "")
	if err != nil {
		t.Fatal(err)
	}
	profile.Model = "gpt-6-luna"
	if _, err := store.SaveAgent(ctx, profile.ID, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAgent(ctx, profile.ID, Agent{Name: "Legacy client"}); err != nil {
		t.Fatal(err)
	}
	profile, err = store.Agent(ctx, DefaultAgentID)
	if err != nil || profile.Model != "gpt-6-luna" {
		t.Fatalf("saved model = %q, %v", profile.Model, err)
	}
	turn, err := store.turn(ctx, queued)
	if err != nil || turn.Agent.Model != "gpt-6.1-sol" {
		t.Fatalf("queued model = %q, %v", turn.Agent.Model, err)
	}
}
