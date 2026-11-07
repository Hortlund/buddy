package main

import (
	"net/url"
	"strings"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func (a *desktop) connectionCard(c *ui.Context, card *tasks.ConnectionCard, task string) {
	panel := ui.Column(c).MinWidth(0).Padding(10, 12).Gap(8).
		Background(c.Theme().Surface).Radius(9).Border(1, c.Theme().Border)
	services := c.Services()
	panel.Children(func() {
		label, mark := tasks.ConnectionSourceName(card.Source), linkMark
		if card.WatchID != "" || card.WatchDraft {
			mark = clockMark
			if card.Source == "" {
				label = "Watches"
			}
		}
		if card.Connection != "" {
			label += " · " + card.Connection
		}
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Icon(c, mark).Size(13, 13).TextColor(c.Theme().TextMuted)
			ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(1)
		})
		ui.Text(c, card.Heading).Role(ui.RoleHeading).FontSize(15).FontWeight(660)
		if card.Detail != "" {
			ui.Text(c, card.Detail).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(4).Selectable()
		}
		connectionExcerpt(c.Key("card-body"), card.Body)
		if len(card.Warnings) != 0 {
			ui.Column(c.Key("warnings")).Gap(3).Children(func() {
				for i, warning := range card.Warnings {
					ui.Text(c.Key(i), warning).FontSize(12).TextColor(c.Theme().Warning).Selectable()
				}
			})
		}
		for i, item := range card.Items {
			ui.Column(c.Key(i)).MinWidth(0).Gap(5).Padding(7, 0, 0).
				BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
				ui.Row(c).AlignItems(ui.Start).Gap(9).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
						ui.Text(c, item.Title).FontSize(13).FontWeight(630).MaxLines(3)
						if item.Detail != "" {
							ui.Text(c, item.Detail).FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
						}
					})
					if item.Badge != "" {
						color := c.Theme().TextMuted
						switch strings.ToLower(item.Badge) {
						case "critical", "high":
							color = c.Theme().Danger
						case "warning":
							color = c.Theme().Warning
						case "unread", "notify in chat":
							color = c.Theme().Success
						}
						ui.Text(c, item.Badge).FontSize(11).FontWeight(620).TextColor(color).
							Padding(3, 6).Background(c.Theme().SurfaceHover).Radius(5).MaxLines(1)
					}
					if item.Value != "" {
						ui.Text(c, item.Value).FontSize(15).FontWeight(650).TextColor(c.Theme().Success).Selectable()
					}
					if validConnectionCardLink(card.Source, item.URL) {
						ui.Button(c.Key("open-item"), "Open").Tooltip("Open " + item.Title).
							OnClick(func() { services.OpenURL(item.URL) })
					}
				})
				connectionExcerpt(c.Key("item-body"), item.Body)
			})
		}
		if card.WatchID != "" || card.WatchDraft || validConnectionCardLink(card.Source, card.URL) {
			ui.Row(c).Gap(6).Wrap().Children(func() {
				if validConnectionCardLink(card.Source, card.URL) {
					label := "Open in " + tasks.ConnectionSourceName(card.Source)
					if card.More {
						label = "See more in " + tasks.ConnectionSourceName(card.Source)
					}
					ui.Button(c.Key("open-source"), label).OnClick(func() { services.OpenURL(card.URL) })
				}
				if card.WatchID != "" {
					if card.More && card.URL == "" {
						ui.Button(c.Key("watch-inbox"), "Open Autonomy inbox").OnClick(func() {
							a.showAutonomy()
							a.autonomy.tab = "Inbox"
						})
					}
					ui.Button(c.Key("manage-watch"), "Manage watches").OnClick(func() {
						a.showAutonomy()
						a.autonomy.tab = "Watches"
					})
				}
				if card.WatchDraft {
					label := "Activate watch"
					if len(card.Items) > 1 {
						label = "Activate watches"
					}
					ui.PrimaryButton(c.Key("activate-watch"), label).
						Disabled(!a.online || a.answeringCard != "" || a.chat.Agent.Removed || a.serverProtocol < api.WatchDraftProtocol).
						OnClick(func() { a.activateChatWatch(task) })
				}
			})
		}
	})
}

func (a *desktop) activateChatWatch(task string) {
	if a.window == nil || !a.online || a.answeringCard != "" {
		return
	}
	a.answeringCard, a.chatErr = task, ""
	win, client, chat := a.window, a.client, a.currentConversation()
	go func() {
		err := client.ActivateChatWatchDraft(a.ctx, chat, task)
		win.Update(func() {
			a.answeringCard = ""
			if chat != a.currentConversation() || client != a.client {
				return
			}
			if err != nil {
				a.chatErr = errorMessage(err, "Couldn’t activate the watch. Try again.")
			}
			a.refreshSoon()
		})
	}()
}

func connectionExcerpt(c *ui.Context, text string) {
	if text == "" {
		return
	}
	preview := ui.Column(c).Gap(4).MinWidth(0)
	expanded := ui.Local(preview, "expanded", func() bool { return false })
	preview.Children(func() {
		body := ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted).Selectable()
		if !*expanded {
			body.MaxLines(4)
		}
		if len(text) > 190 || strings.Count(text, "\n") >= 3 {
			label := "Show details"
			if *expanded {
				label = "Hide details"
			}
			ui.Button(c.Key("expand"), label).AlignSelf(ui.Start).OnClick(func() { *expanded = !*expanded })
		}
	})
}

func validConnectionCardLink(source, link string) bool {
	u, err := url.Parse(link)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return false
	}
	switch source {
	case tasks.GitHubCI:
		return validGitHubLink(link)
	case tasks.GmailLabel:
		return u.Scheme == "https" && u.Host == "mail.google.com"
	case tasks.GrafanaAlerts, tasks.PrometheusAlerts:
		return u.Scheme == "https" || u.Scheme == "http"
	}
	return false
}
