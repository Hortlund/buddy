package tasks

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPrimaryBudAndGroupLeadPersistWithoutReassigningTurns(t *testing.T) {
	ctx, db := t.Context(), filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if directory, err := store.Directory(ctx); err != nil || directory.PrimaryAgentID != DefaultAgentID {
		t.Fatalf("initial primary = %q, %v", directory.PrimaryAgentID, err)
	}
	look := DefaultAvatar()
	look.Color, look.Shape = "teal", "cloud"
	forge, err := store.CreateAgent(ctx, Agent{Name: "Forge", Role: "Developer", Avatar: &look})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPrimaryAgent(ctx, forge.ID); err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Workshop", Members: []string{DefaultAgentID, forge.ID}})
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := store.Send(ctx, group.ID, "First", "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupLead(ctx, group.ID, DefaultAgentID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupLead(ctx, group.ID, "missing"); err == nil {
		t.Fatal("accepted a lead outside the group")
	}
	if err := store.SetGroupLead(ctx, DefaultConversationID, forge.ID); err == nil {
		t.Fatal("changed the owner of a direct chat")
	}
	if task, created, err := store.Send(ctx, group.ID, "First", "first"); err != nil || created || task.ID != before.ID {
		t.Fatalf("retry after changing the lead: %+v, created %v, %v", task, created, err)
	}
	if _, _, err := store.SendTo(ctx, group.ID, DefaultAgentID, "First", "first"); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry with a different explicit recipient: %v", err)
	}
	after, _, err := store.Send(ctx, group.ID, "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	explicit, _, err := store.SendTo(ctx, group.ID, forge.ID, "Third", "third")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	directory, err := reopened.Directory(ctx)
	if err != nil || directory.PrimaryAgentID != forge.ID || len(directory.Agents) != 2 || directory.Agents[0].ID != forge.ID {
		t.Fatalf("saved primary directory: %+v, %v", directory, err)
	}
	if avatar := directory.Agents[0].Avatar; avatar == nil || avatar.Color != look.Color || avatar.Shape != look.Shape {
		t.Fatalf("avatar in Bud directory: %+v", avatar)
	}
	chat, err := reopened.Conversation(ctx, group.ID, 0)
	if err != nil || chat.Agent.ID != DefaultAgentID || len(chat.Members) != 2 {
		t.Fatalf("saved group lead or membership: %+v, %v", chat, err)
	}
	for _, item := range []struct {
		task Task
		id   string
	}{{before, forge.ID}, {after, DefaultAgentID}, {explicit, forge.ID}} {
		turn, err := reopened.turn(ctx, item.task)
		if err != nil || turn.Agent.ID != item.id {
			t.Fatalf("task %s assigned to %q, want %q, err %v", item.task.ID, turn.Agent.ID, item.id, err)
		}
	}
}
