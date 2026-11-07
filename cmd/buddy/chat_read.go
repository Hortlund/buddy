package main

import (
	"time"

	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

type chatReadProgress struct {
	shown, saved int64
	saving       bool
	retryAt      time.Time
}

type transcriptFrame struct {
	id       string
	messages []tasks.Message
}

type chatViewport struct {
	frameAt           time.Time
	previous, drawing transcriptFrame
	followingThrough  int64
}

func (a *desktop) observeChat(c *ui.Context, focused bool) {
	v := &a.chatViewport
	if v.frameAt != c.Now() {
		v.frameAt, v.previous = c.Now(), v.drawing
	}
	v.drawing, v.followingThrough = transcriptFrame{}, 0
	id, messages := a.chat.ID, a.chat.Messages
	if a.section != "" || a.showingWorkspaceFiles() || a.onboarding || (a.voice != nil && a.voice.expanded) || id == "" || len(messages) == 0 {
		return
	}
	v.drawing = transcriptFrame{id, messages}
	if !focused {
		return
	}

	// MyGo reports the previous layout. Use the messages from that frame,
	// since a poll or a history load can move messages to different rows.
	previous := v.previous
	first, last := a.list.Visible()
	if previous.id == id {
		end := min(last+1, len(previous.messages))
		if first >= 0 && first < end {
			through := latestAssistantID(previous.messages[first:end])
			if read := a.chatReads[id]; through > read.shown {
				if a.chatReads == nil {
					a.chatReads = make(map[string]chatReadProgress)
				}
				read.shown = through
				a.chatReads[id] = read
			}
		}
	}
	if last < first || a.list.AtEnd() {
		// The list follows its end. Hide the dot for the incoming reply in
		// this frame, then wait for its layout before sending a read receipt.
		v.followingThrough = latestAssistantID(messages)
	}
	if previous.id != id || previous.messages[len(previous.messages)-1].ID != messages[len(messages)-1].ID {
		c.AnimationFrame()
	}
}

func latestAssistantID(messages []tasks.Message) int64 {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].ID
		}
	}
	return 0
}

func (a *desktop) chatHasUnread(id string, unread bool, through int64) bool {
	if !unread || (through != 0 && through <= a.chatReads[id].shown) {
		return false
	}
	following := a.chatViewport.followingThrough
	return id != a.currentConversation() || following == 0 || through > following
}

func (a *desktop) sendChatReadReceipts(c *ui.Context) {
	if !a.online || a.window == nil {
		return
	}
	for id, read := range a.chatReads {
		if read.saving || read.saved >= read.shown {
			continue
		}
		if delay := time.Until(read.retryAt); delay > 0 {
			c.After(delay)
			continue
		}
		read.saving = true
		a.chatReads[id] = read
		win, client, ctx, through := a.window, a.client, a.ctx, read.shown
		go func() {
			err := client.MarkResultsRead(ctx, id, through)
			win.Update(func() {
				read := a.chatReads[id]
				read.saving = false
				if err == nil {
					read.saved, read.retryAt = through, time.Time{}
				} else {
					read.retryAt = time.Now().Add(3 * time.Second)
				}
				a.chatReads[id] = read
			})
		}()
	}
}
