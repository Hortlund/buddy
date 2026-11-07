package main

import (
	"strconv"
	"testing"
	"time"

	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func TestUnreadDotFollowsRenderedChat(t *testing.T) {
	const chatID = "ben-chat"
	a := &desktop{conversationID: chatID, online: true}
	a.chat = tasks.Conversation{ID: chatID, Agent: tasks.Agent{Name: "Ben"}}
	a.directory.Agents = []tasks.AgentRef{{Name: "Ben", ConversationID: chatID}}
	a.directory.Groups = []tasks.ChatRef{{ID: "team", Name: "Team", HasNewResult: true, UnreadThrough: 99}}
	addReply := func(id int64) {
		text := "Reply " + strconv.FormatInt(id, 10)
		a.chat.Messages = append(a.chat.Messages, tasks.Message{ID: id, TaskID: text, Role: "assistant", Text: text})
	}
	for id := int64(1); id <= 30; id++ {
		addReply(id)
	}
	focused := true
	view := ui.NewTester(func(c *ui.Context) {
		a.observeChat(c, focused)
		ui.Row(c).Fill().Children(func() {
			a.sidebar(c, 44, 0)
			ui.Column(c).Grow(1).FillHeight().Children(func() {
				if a.section == "" {
					a.transcript(c)
				}
			})
		})
	}, 780, 350)
	nextFrame := func() {
		view.Advance(20 * time.Millisecond)
		view.Frame()
	}
	assertRead := func(want int64) {
		t.Helper()
		if got := a.chatReads[chatID].shown; got != want {
			t.Fatalf("read through %d, want %d", got, want)
		}
	}
	assertDot := func(name string, want bool) {
		t.Helper()
		if _, got := view.Find(name + ", unread activity"); got != want {
			t.Fatalf("%s unread label present = %v, want %v", name, got, want)
		}
	}
	setUnread := func(id int64) {
		a.directory.Agents[0].HasNewResult = true
		a.directory.Agents[0].UnreadThrough = id
	}
	nextFrame()
	assertRead(30)
	assertDot("Team", true)

	a.list.ScrollTo(0, ui.Start)
	view.Frame()
	nextFrame()
	if a.list.AtEnd() {
		t.Fatal("test failed to scroll away from the latest reply")
	}
	addReply(31)
	setUnread(31)
	nextFrame()
	nextFrame()
	assertRead(30)
	assertDot("Ben", true)

	a.list.ScrollToEnd()
	view.Frame()
	nextFrame()
	assertRead(31)
	assertDot("Ben", false) // Directory still reports 31 unread.

	a.chat.Active = &tasks.ActiveRun{ID: "Reply 32", Status: tasks.Running}
	nextFrame()
	a.chat.Active = nil
	addReply(32)
	setUnread(32)
	view.Frame()
	assertRead(31) // The new reply had not been laid out when the frame began.
	assertDot("Ben", false)
	nextFrame()
	assertRead(32)

	setUnread(33) // A later directory snapshot knows about a reply not yet fetched.
	nextFrame()
	assertDot("Ben", true)
	addReply(33)
	focused = false
	nextFrame()
	nextFrame()
	assertRead(32)
	assertDot("Ben", true)
	focused = true
	nextFrame()
	assertRead(33)
	assertDot("Ben", false)
	a.section = "preferences"
	nextFrame()
	assertDot("Ben", false) // A stale poll also stays hidden after leaving the chat.
}
