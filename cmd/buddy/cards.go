package main

import (
	"fmt"

	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func (a *desktop) choiceCard(c *ui.Context, message tasks.Message) {
	card := message.Card
	if card.Confirmation != "" && len(card.Options) == 2 {
		a.confirmationCard(c, message)
		return
	}
	theme := c.Theme()
	ui.Column(c.Key("choice-card")).FillWidth().MinWidth(0).Gap(9).Padding(11).
		Radius(11).Border(1, theme.Border).Background(theme.Surface).Children(func() {
		label := "Your choice"
		if card.Choice != nil {
			label = "Answered"
		} else if a.answeringCard == message.TaskID {
			label = "Sending answer…"
		}
		ui.Row(c).Gap(7).Children(func() {
			ui.Icon(c, buddyMark).Size(14, 14).TextColor(theme.TextMuted)
			ui.Text(c, label).FontSize(11).FontWeight(670).TextColor(theme.TextMuted)
		})
		ui.Text(c, card.Question).Role(ui.RoleHeading).FontSize(15).FontWeight(670).Selectable()
		if card.Detail != "" {
			messageMarkdown(c.Key("choice-context"), card.Detail)
		}
		for i, option := range card.Options {
			chosen := card.Choice != nil && *card.Choice == i
			note := ""
			if i < len(card.OptionDetails) {
				note = card.OptionDetails[i]
			}
			btn := ui.ButtonBase(c.Key(fmt.Sprintf("choice-%s-%d", message.TaskID, i))).
				Label(option).Description(note).Selected(chosen).
				Disabled(!a.online || a.answeringCard != "" || card.Choice != nil || a.chat.Agent.Removed || a.budWasRemoved(message.AgentID)).
				MinHeight(36).FillWidth().Justify(ui.Start).Gap(8).Padding(7, 9).
				Radius(8).Border(1, theme.Border).Background(theme.SurfaceHover)
			if chosen {
				btn.Border(1, theme.Success).Background(theme.Success.Alpha(.12)).Opacity(1)
			} else if btn.Hovered() && card.Choice == nil {
				btn.Border(1, theme.Accent).Background(theme.SurfacePressed)
			}
			btn.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, option).MinWidth(0).FontSize(13).FontWeight(620).TextColor(theme.Text)
					if note != "" {
						ui.Text(c, note).MinWidth(0).FontSize(12).TextColor(theme.TextMuted)
					}
				})
				if chosen {
					ui.Text(c, "✓ Selected").FontSize(11).TextColor(theme.Success).Shrink(0)
				} else if card.Choice == nil {
					ui.Icon(c, arrowMark).Size(12, 12).Shrink(0).TextColor(theme.TextMuted)
				}
			}).OnClick(func() { a.answerCard(message.TaskID, i) })
		}
	})
}

func (a *desktop) confirmationCard(c *ui.Context, message tasks.Message) {
	card, theme := message.Card, c.Theme()
	disabled := !a.online || a.answeringCard != "" || a.chat.Agent.Removed || a.budWasRemoved(message.AgentID)
	ui.Column(c.Key("confirmation-card")).FillWidth().MinWidth(0).Gap(8).Padding(11).
		Radius(10).Border(1, theme.Border).Background(theme.Surface).Children(func() {
		ui.Text(c, card.Question).Role(ui.RoleHeading).FontSize(15).FontWeight(670).Selectable()
		if card.Detail != "" {
			ui.Text(c, card.Detail).FontSize(12).TextColor(theme.TextMuted).Selectable()
		}
		if card.Choice != nil {
			label := "Confirmed"
			if *card.Choice == 1 {
				label = "Cancelled"
			}
			ui.Text(c, label).FontSize(12).TextColor(theme.TextMuted)
			return
		}
		ui.Row(c).FillWidth().Gap(7).Justify(ui.End).Children(func() {
			ui.Button(c.Key("confirmation-cancel"), card.Options[1]).Disabled(disabled).
				OnClick(func() { a.answerCard(message.TaskID, 1) })
			if card.Confirmation == "destructive" {
				ui.Button(c.Key("confirmation-accept"), card.Options[0]).TextColor(theme.Danger).Disabled(disabled).
					OnClick(func() { a.answerCard(message.TaskID, 0) })
			} else {
				ui.PrimaryButton(c.Key("confirmation-accept"), card.Options[0]).Disabled(disabled).
					OnClick(func() { a.answerCard(message.TaskID, 0) })
			}
		})
	})
}

func (a *desktop) answerCard(card string, choice int) {
	if a.window == nil || !a.online || a.answeringCard != "" {
		return
	}
	a.answeringCard, a.chatErr = card, ""
	win, chat := a.window, a.currentConversation()
	go func() {
		err := a.client.AnswerCard(a.ctx, chat, card, choice)
		win.Update(func() {
			a.answeringCard = ""
			if chat != a.currentConversation() {
				return
			}
			if err != nil {
				a.chatErr = errorMessage(err, "Couldn't save that answer. Try again.")
			}
			a.refreshSoon()
		})
	}()
}
