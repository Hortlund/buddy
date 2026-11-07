package tasks

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestBudReactionCanCompleteAChatTurn(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request, _, err := store.Send(ctx, DefaultConversationID, "Thanks!", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	var action TurnAction
	turn := Turn{coordinate: true, action: &action}
	reply, err := turn.decodeReply(`{"reply":"","reaction":"👍"}`, nil)
	if err != nil || reply != "" || action.Reaction != "👍" {
		t.Fatalf("decoded reply = %q, reaction = %q, error = %v", reply, action.Reaction, err)
	}
	if err := store.finishTurn(ctx, request.ID, Succeeded, "Replied.", reply, &action); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 1 || chat.Active != nil || chat.ReactionRevision == 0 {
		t.Fatalf("reaction-only chat = %+v, %v", chat, err)
	}
	message := chat.Messages[0]
	if message.Role != "user" || message.Status != Succeeded || len(message.Reactions) != 1 ||
		message.Reactions[0].Emoji != "👍" || message.Reactions[0].AgentID != DefaultAgentID {
		t.Fatalf("reacted message = %+v", message)
	}
	if _, err := store.SetReaction(ctx, DefaultConversationID, message.ID, "❤️"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner reacted to own message: %v", err)
	}
}

func TestReactionsSyncOnOlderMessages(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	reply := func() {
		t.Helper()
		request, _, err := store.Send(ctx, DefaultConversationID, "Question", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.claim(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.finish(ctx, request.ID, Succeeded, "Replied.", "Answer"); err != nil {
			t.Fatal(err)
		}
	}
	reply()
	initial, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(initial.Messages) != 2 {
		t.Fatalf("initial conversation = %+v, %v", initial, err)
	}
	target := initial.Messages[1].ID
	for range 60 {
		reply()
	}
	checkUpdate := func(after int64, emoji string) ReactionUpdate {
		t.Helper()
		chat, err := store.ConversationWithReactions(ctx, DefaultConversationID, 0, after)
		if err != nil || chat.Before <= target || len(chat.ReactionUpdates) != 1 {
			t.Fatalf("older reaction updates = %+v, before = %d, error = %v", chat.ReactionUpdates, chat.Before, err)
		}
		update := chat.ReactionUpdates[0]
		if update.MessageID != target || update.Revision != chat.ReactionRevision || update.Revision <= after {
			t.Fatalf("unexpected update = %+v, cursor = %d", update, chat.ReactionRevision)
		}
		if emoji == "" && len(update.Reactions) != 0 || emoji != "" && (len(update.Reactions) != 1 ||
			update.Reactions[0].Emoji != emoji || update.Reactions[0].AgentID != "") {
			t.Fatalf("reactions = %+v, want %q", update.Reactions, emoji)
		}
		return update
	}
	if _, err := store.SetReaction(ctx, DefaultConversationID, target, "❤️"); err != nil {
		t.Fatal(err)
	}
	added := checkUpdate(0, "❤️")
	if same, err := store.SetReaction(ctx, DefaultConversationID, target, "❤️"); err != nil || same.Revision != added.Revision {
		t.Fatalf("unchanged reaction = %+v, %v", same, err)
	}
	if _, err := store.SetReaction(ctx, DefaultConversationID, target, "🎉"); err != nil {
		t.Fatal(err)
	}
	replaced := checkUpdate(added.Revision, "🎉")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReaction(ctx, DefaultConversationID, target, ""); err != nil {
		t.Fatal(err)
	}
	checkUpdate(replaced.Revision, "")
}
