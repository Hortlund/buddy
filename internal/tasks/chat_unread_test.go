package tasks

import (
	"path/filepath"
	"testing"
)

func TestChatUnreadTracksNewRepliesAndPreservesOldNotifications(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	open := func() *Store {
		store, err := OpenStore(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	teammate, err := store.CreateAgent(ctx, Agent{Name: "Forge"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Workshop", Members: []string{DefaultAgentID, teammate.ID}})
	if err != nil {
		t.Fatal(err)
	}
	send := func(chat, text string) Task {
		task, _, err := store.Send(ctx, chat, text, "")
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	reply := func(task Task) int64 {
		if err := addReply(ctx, store.db, task.ID, "Here’s a reply."); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := store.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE task_id=? AND role='assistant'", task.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	assertUnread := func(direct, team int64) {
		t.Helper()
		directory, err := store.Directory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if directory.Agents[0].UnreadThrough != direct || directory.Groups[0].UnreadThrough != team ||
			directory.Agents[0].HasNewResult != (direct != 0) || directory.Groups[0].HasNewResult != (team != 0) ||
			directory.Agents[1].HasNewResult {
			t.Fatalf("unread mismatch, want direct=%d and group=%d: %+v", direct, team, directory)
		}
	}
	reply(send(DefaultConversationID, "Old conversation"))
	notification := send(group.ID, "Old watch notification")
	noticeID := reply(notification)
	if _, err := store.db.ExecContext(ctx, "UPDATE turn_details SET origin=? WHERE task_id=?", WatchNoticeOrigin, notification.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "DROP TABLE chat_unread_baseline"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	assertUnread(0, noticeID)

	request := send(DefaultConversationID, "New question")
	assertUnread(0, noticeID)
	firstID := reply(request)
	secondID := reply(send(DefaultConversationID, "One more thing"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	assertUnread(secondID, noticeID)
	if err := store.MarkResultsRead(ctx, DefaultConversationID, firstID); err != nil {
		t.Fatal(err)
	}
	assertUnread(secondID, noticeID)
	if err := store.MarkResultsRead(ctx, DefaultConversationID, secondID); err != nil {
		t.Fatal(err)
	}
	assertUnread(0, noticeID)
	if err := store.MarkResultsRead(ctx, group.ID, noticeID); err != nil {
		t.Fatal(err)
	}
	assertUnread(0, 0)
	newGroupID := reply(send(group.ID, "New group question"))
	assertUnread(0, newGroupID)
}
