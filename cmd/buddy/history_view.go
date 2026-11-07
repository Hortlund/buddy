package main

import (
	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

var backMark = svg(`<path d="m14 6-6 6 6 6"/>`)
var historyMark = svg(`<path d="M4 8h16v12H4zM3 4h18v4H3zm7 8h4"/>`)

type historyView struct {
	chat  string
	title string
	calls []tasks.CallHistory
	busy  bool
	err   string
}

func (a *desktop) pageNavigation(c *ui.Context, height float32) {
	const inset = 16
	ui.Row(c).Height(height).Shrink(0).FillWidth().Padding(0, max(inset, c.TitleBar().Right), 0, inset).
		Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).DragWindow().Children(func() {
		parent, title := "", "Back to chat"
		switch {
		case a.section == "agent" && a.editReturn == "buds":
			parent, title = "buds", "Back to Buds"
		case a.section != "menu" && a.section != "routine" && !(a.section == "history" && a.history.chat != ""):
			parent, title = "menu", "Back to menu"
		}
		enabled := !a.saving && !a.sending
		quietControl(c.Key("page-back"), title, backMark, enabled, func() {
			a.section = parent
			a.refreshSoon()
		})
		if parent != "" {
			ui.Box(c).Grow(1)
			quietControl(c.Key("page-return-chat"), "Return to chat", buddyMark, enabled, func() {
				a.section = ""
				a.refreshSoon()
			})
		}
	})
}

func (a *desktop) conversationHistoryMenu(c *ui.Context) {
	if a.chat.ID == "" || a.serverProtocol < api.ConversationHistoryProtocol {
		return
	}
	btn := ui.ButtonBase(c.Key("chat-history-menu")).Size(28, 28).Label("Chat history options").
		Tooltip("Chat history options").Disabled(!a.online || a.history.busy || a.sending)
	btn.PopoverMenuButton(func(menu *ui.Menu) {
		if menu.Item("Call history").Chosen() {
			a.openHistory(a.currentConversation())
		}
		if a.chat.Name != "" && a.voice == nil {
			label := "Archive group chat"
			if a.chat.Archived {
				label = "Restore group chat"
			}
			if menu.Item(label).Disabled(!a.canChangeHistory()).Chosen() {
				a.changeHistory(a.currentConversation(), "", !a.chat.Archived)
			}
		}
	})
	btn.Children(func() { ui.Icon(c, moreMark).Size(16, 16).TextColor(c.Theme().TextMuted) })
}

func (a *desktop) openHistory(chat string) {
	a.section = "history"
	a.history = historyView{chat: chat}
	if chat != "" {
		if a.chat.ID == chat {
			a.history.title = a.chat.Name
			if a.history.title == "" {
				a.history.title = a.chat.Agent.Name
			}
		} else {
			for _, group := range a.directory.Groups {
				if group.ID == chat {
					a.history.title = group.Name
					break
				}
			}
		}
	}
	if chat == "" || a.window == nil || !a.online {
		return
	}
	a.history.busy = true
	win, client := a.window, a.client
	go func() {
		items, err := client.Calls(a.ctx, chat)
		win.Update(func() {
			if a.section != "history" || a.history.chat != chat {
				return
			}
			a.history.busy, a.history.calls = false, items
			if err != nil {
				a.history.err = errorMessage(err, "Couldn’t load call history.")
			}
		})
	}()
}

func (a *desktop) canChangeHistory() bool {
	return a.online && !a.historyChanging && !a.history.busy && !a.sending && !a.saving && a.voice == nil &&
		a.serverProtocol >= api.ConversationHistoryProtocol
}

func (a *desktop) changeHistory(chat, call string, archived bool) {
	if !a.canChangeHistory() || a.window == nil {
		return
	}
	a.historyChanging, a.history.err = true, ""
	win, client := a.window, a.client
	go func() {
		var err error
		if call == "" {
			err = client.SetGroupArchived(a.ctx, chat, archived)
		} else {
			err = client.SetCallArchived(a.ctx, chat, call, archived)
		}
		win.Update(func() {
			a.historyChanging = false
			if err != nil {
				a.historyNotice = ui.Toast{Title: "Couldn’t update history", Description: errorMessage(err, "Try again in a moment."), Type: "error"}
				return
			}
			notice := "Group archived"
			if call != "" {
				notice = "Call archived"
			}
			if !archived {
				notice = "Group restored"
				if call != "" {
					notice = "Call restored"
				}
			}
			a.historyNotice = ui.Toast{Title: notice}
			if archived {
				a.historyNotice.Description = "Restore it in Call history."
				if call == "" {
					a.historyNotice.Description = "Restore it in Menu → Archived chats. Its routines and watches are paused."
				}
			}
			if call != "" {
				for i := range a.history.calls {
					if a.history.chat == chat && a.history.calls[i].ID == call {
						a.history.calls[i].Archived = archived
					}
				}
				if a.currentConversation() == chat {
					// Reload rather than merge cached pages containing old captions.
					a.chat.Messages, a.chat.Before = nil, 0
					a.chatViewport, a.list = chatViewport{}, ui.ListState{}
				}
			} else if a.currentConversation() == chat {
				a.chat.Archived = archived
				if archived && a.section == "" {
					a.openConversation(primaryConversation(a.directory))
				}
			}
			a.refreshSoon()
		})
	}()
}

func (a *desktop) historyPage(c *ui.Context) {
	v := &a.history
	title, subtitle := "Archived chats", "Open or restore an archived group. Its routines and watches stay paused until you resume them."
	if v.chat != "" {
		title, subtitle = "Call history", "Archive a call to hide its captions from the chat. You can restore it here."
		if v.title != "" {
			subtitle = v.title + " · Archive a call to hide its captions."
		}
	}
	controlPage(c.Key("conversation-history"), title, subtitle, nil, func() {
		if v.err != "" {
			ui.Text(c, v.err).TextColor(c.Theme().Danger)
		}
		if v.chat == "" {
			if len(a.directory.ArchivedGroups) == 0 {
				controlEmpty(c, historyMark, "No archived groups", "Archived group chats appear here.")
			}
			for _, item := range a.directory.ArchivedGroups {
				a.historyRow(c.Key(item.ID), item.Name, "Archived group", "Restore group chat", func() {
					a.changeHistory(item.ID, "", false)
				}, func() { a.openConversation(item.ID) })
			}
			return
		}
		if v.busy {
			ui.Text(c, "Loading calls…").TextColor(c.Theme().TextMuted)
		} else if len(v.calls) == 0 && v.err == "" {
			controlEmpty(c, callMark, "No saved calls", "Captions from calls in this chat will appear here.")
		}
		for _, call := range v.calls {
			status, action := "Saved captions", "Archive call"
			if call.Archived {
				status, action = "Archived captions", "Restore call"
			}
			chat := v.chat
			a.historyRow(c.Key(call.ID), call.StartedAt.Local().Format("Jan 2, 2006 · 15:04:05"), status, action, func() {
				a.changeHistory(chat, call.ID, !call.Archived)
			}, nil)
		}
	})
}

func (a *desktop) historyRow(c *ui.Context, title, detail, action string, change, open func()) {
	connectionSection(c, func() {
		ui.Row(c).FillWidth().Padding(11).Gap(8).AlignItems(ui.Center).ContextMenu(func(menu *ui.Menu) {
			if open != nil && menu.Item("Open chat").Chosen() {
				open()
			}
			if menu.Item(action).Disabled(!a.canChangeHistory()).Chosen() {
				change()
			}
		}).Children(func() {
			connectionDetail(c, clockMark, title, detail)
			if open != nil {
				ui.Button(c.Key("open"), "Open").OnClick(open)
			}
			ui.Button(c.Key("change"), action).Disabled(!a.canChangeHistory()).OnClick(change)
		})
	})
}
