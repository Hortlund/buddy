package main

import (
	"sort"

	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

var reactionMark = svg(`<path d="M19 12a8 8 0 1 1-7-8m6-1v6m-3-3h6M8 13a4 4 0 0 0 7 0M9 9h.01m4 0h.01"/>`)

func applyReactionUpdates(messages []tasks.Message, updates []tasks.ReactionUpdate) {
	for _, update := range updates {
		i := sort.Search(len(messages), func(i int) bool { return messages[i].ID >= update.MessageID })
		if i < len(messages) && messages[i].ID == update.MessageID && messages[i].ReactionRevision < update.Revision {
			messages[i].Reactions, messages[i].ReactionRevision = update.Reactions, update.Revision
		}
	}
}

func (a *desktop) canReact(message tasks.Message) bool {
	return message.ID != 0 && message.Role == "assistant" && len(a.chat.ReactionOptions) != 0 &&
		a.online && !a.chat.Archived && !a.chat.Agent.Removed && a.reactingMessage == 0
}

func (a *desktop) setReaction(message tasks.Message, emoji string) {
	if a.window == nil || !a.canReact(message) {
		return
	}
	a.reactingMessage = message.ID
	win, client, chat := a.window, a.client, a.currentConversation()
	go func() {
		update, err := client.SetReaction(a.ctx, chat, message.ID, emoji)
		win.Update(func() {
			a.reactingMessage = 0
			if a.currentConversation() != chat {
				return
			}
			if err != nil {
				a.chatErr = errorMessage(err, "Couldn’t save the reaction.")
				return
			}
			applyReactionUpdates(a.chat.Messages, []tasks.ReactionUpdate{update})
			a.refreshSoon()
		})
	}()
}

func (a *desktop) reactionMenu(message tasks.Message, menu *ui.Menu) {
	var selected string
	for _, reaction := range message.Reactions {
		if reaction.AgentID == "" {
			selected = reaction.Emoji
		}
	}
	for _, option := range a.chat.ReactionOptions {
		if menu.Item(option.Emoji + "  " + option.Label).Checked(option.Emoji == selected).Chosen() {
			emoji := option.Emoji
			if emoji == selected {
				emoji = ""
			}
			a.setReaction(message, emoji)
		}
	}
}

func (a *desktop) reactionButton(c *ui.Context, message tasks.Message) {
	button := ui.ButtonBase(c).Label("React to message").Tooltip("React to message").Size(24, 24).
		Shrink(0).Center().Radius(6).Disabled(!a.canReact(message))
	color := c.Theme().TextMuted
	if button.Hovered() {
		button.Background(c.Theme().SurfaceHover)
	} else {
		color = color.Alpha(.72)
	}
	button.PopoverMenuButton(func(menu *ui.Menu) { a.reactionMenu(message, menu) })
	button.Children(func() { ui.Icon(c, reactionMark).Size(15, 15).TextColor(color) })
}

func (a *desktop) messageReactions(c *ui.Context, message tasks.Message, align ui.Align) {
	if len(message.Reactions) == 0 {
		return
	}
	ui.Row(c).AlignSelf(align).Wrap().Gap(4).Children(func() {
		for _, reaction := range message.Reactions {
			if reaction.AgentID == "" {
				ui.Button(c.Key("my-reaction"), reaction.Emoji).Height(24).Padding(0, 6).Radius(9).
					Label("Remove your reaction " + reaction.Emoji).Tooltip("Remove your reaction").
					Background(c.Theme().SurfacePressed).Disabled(!a.canReact(message)).
					OnClick(func() { a.setReaction(message, "") })
				continue
			}
			label := reaction.AgentName + " reacted " + reaction.Emoji
			ui.Text(c.Key(reaction.AgentID), reaction.Emoji).FontSize(13).Padding(2, 6).Radius(9).
				Background(c.Theme().SurfaceHover).Label(label).Tooltip(label)
		}
	})
}
