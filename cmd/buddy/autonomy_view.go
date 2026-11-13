package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

type autonomyView struct {
	tab                     string
	next                    time.Time
	loading                 bool
	busy                    bool
	err                     string
	watches                 []tasks.Watch
	inbox                   []tasks.InboxItem
	health                  tasks.StandingHealth
	memories                []tasks.MemoryNote
	agent                   string
	draft                   tasks.Watch
	budget                  string
	scopes                  watchScopes
	memory                  tasks.MemoryNote
	expiry                  string
	docs                    tasks.BudDocuments
	soulDraft, soulRevision string
	memoryPanel             string
	clearingMemory          bool
	editingMemory           bool
}

func (a *desktop) showAutonomy() {
	a.section = "autonomy"
	if a.autonomy.tab == "" {
		a.autonomy.tab = "Inbox"
	}
	a.syncMemoryBud()
	a.autonomy.next = time.Time{}
}

func (v *autonomyView) selectMemoryBud(id string) {
	v.agent, v.next, v.memory, v.memories, v.expiry = id, time.Time{}, tasks.MemoryNote{}, nil, ""
	v.docs, v.soulDraft, v.soulRevision, v.clearingMemory, v.editingMemory = tasks.BudDocuments{}, "", "", false, false
}

func (a *desktop) syncMemoryBud() {
	v := &a.autonomy
	if v.agent != "" && (!a.budWasRemoved(v.agent) || v.memory != (tasks.MemoryNote{}) || v.expiry != "" || v.soulDraft != v.docs.Soul) {
		return
	}
	profile := a.focusedAgent()
	id := profile.ID
	if id == "" || profile.Removed || a.budWasRemoved(id) {
		id = a.directory.PrimaryAgentID
	}
	if id != v.agent {
		v.selectMemoryBud(id)
	}
}

func (a *desktop) refreshAutonomy(c *ui.Context) {
	a.syncMemoryBud()
	v := &a.autonomy
	if a.window == nil || v.loading || v.busy || !a.online || a.serverProtocol < api.StandingProtocol {
		return
	}
	if wait := time.Until(v.next); wait > 0 {
		c.After(wait)
		return
	}
	v.loading, v.next = true, time.Now().Add(5*time.Second)
	win, client, tab, bud, protocol := a.window, a.client, v.tab, v.agent, a.serverProtocol
	go func() {
		var data autonomyView
		var err error
		switch tab {
		case "Watches":
			data.watches, err = client.Watches(a.ctx)
			if err == nil {
				data.health, _ = client.StandingHealth(a.ctx)
			}
		case "Memory":
			if bud != "" {
				data.memories, err = client.Memories(a.ctx, bud)
				if err == nil && protocol >= api.ConversationHistoryProtocol {
					data.docs, err = client.BudDocuments(a.ctx, bud)
				}
			}
		case "Health":
			data.health, err = client.StandingHealth(a.ctx)
		default:
			data.inbox, err = client.Inbox(a.ctx)
		}
		win.Update(func() {
			v.loading = false
			if v.busy || v.tab != tab || tab == "Memory" && v.agent != bud {
				v.next = time.Time{}
				return
			}
			if err != nil {
				v.err = errorMessage(err, "Couldn't load Autonomy. Retrying…")
				return
			}
			switch tab {
			case "Watches":
				v.watches = data.watches
				v.health.GitHub = data.health.GitHub
			case "Memory":
				v.memories = data.memories
				if data.docs.Revision != "" {
					if v.soulRevision == "" || v.soulDraft == v.docs.Soul && v.soulRevision == v.docs.Revision {
						v.soulDraft, v.soulRevision = data.docs.Soul, data.docs.Revision
					}
					v.docs = data.docs
				}
			case "Health":
				v.health = data.health
			default:
				v.inbox = data.inbox
			}
		})
	}()
}

func (a *desktop) changeAutonomy(save func(context.Context, *api.Client) error, done func()) {
	v := &a.autonomy
	if a.window == nil || v.busy || !a.online {
		return
	}
	v.busy, v.err = true, ""
	win, client := a.window, a.client
	go func() {
		err := save(a.ctx, client)
		win.Update(func() {
			v.busy = false
			if err != nil {
				v.err = errorMessage(err, "Couldn't save the change.")
				return
			}
			v.next = time.Time{}
			if done != nil {
				done()
			}
		})
	}()
}

func (a *desktop) autonomyPage(c *ui.Context) {
	a.refreshAutonomy(c)
	a.refreshIntegrations(c)
	v := &a.autonomy
	ready := a.online && a.serverProtocol >= api.StandingProtocol
	controlPage(c.Key("autonomy-page"), "Autonomy", "Review what your Buds do and remember between chats.", func() {
		if !ready {
			return
		}
		ui.Row(c).FillWidth().Gap(4).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			for _, tab := range []struct {
				label string
				icon  *ui.SVG
			}{{"Inbox", buddyMark}, {"Watches", clockMark}, {"Memory", fileMark}, {"Health", hostMark}} {
				controlTab(c.Key("autonomy-"+strings.ToLower(tab.label)), tab.label, tab.icon, v.tab == tab.label, func() {
					v.tab, v.err, v.next = tab.label, "", time.Time{}
				})
			}
		})
	}, func() {
		if !ready {
			controlEmpty(c, hostMark, "Autonomy is unavailable", "Connect to an up-to-date Buddy server.")
			return
		}
		if v.err != "" {
			ui.Text(c, v.err).TextColor(c.Theme().Danger)
		}
		ui.Column(c.Key(v.tab)).FillWidth().MinWidth(0).Gap(11).Children(func() {
			switch v.tab {
			case "Watches":
				a.watchSettings(c)
			case "Memory":
				a.autonomyMemory(c)
			case "Health":
				a.autonomyHealth(c)
			default:
				a.autonomyInbox(c)
			}
		})
	})
}

func (a *desktop) watchSettings(c *ui.Context) {
	v := &a.autonomy
	if v.health.GitHub == "webhook" {
		ui.Text(c, "GitHub is using live webhooks. Add an Actions read token for catch-up, or manually redeliver events missed while Buddy was offline.").
			TextColor(c.Theme().TextMuted)
	}
	controlSection(c, "Watches", "Follow activity from your connected services.", func() {
		if v.draft.Source != "" {
			return
		}
		if choices := a.availableWatchConnections(); len(choices) != 0 {
			ui.PrimaryButton(c.Key("new-watch"), "New watch").OnClick(func() { a.newConnectionWatch(choices[0]) })
		} else {
			ui.Button(c.Key("connect-watch-source"), "Connect a service").OnClick(func() { a.showConnectionTab(connectionsIntegrations) })
		}
	})
	if v.draft.Source != "" {
		connectionSection(c, func() {
			ui.Column(c).Padding(13).Gap(9).Children(func() {
				title, submit := "New watch", "Create watch"
				if v.draft.ID != "" {
					title, submit = "Edit watch", "Save watch"
				}
				ui.Text(c, title).FontWeight(680)
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, "Report to").TextColor(c.Theme().TextMuted)
					ui.MenuButton(c.Key("watch-conversation"), a.watchChatName(v.draft.ConversationID), func(m *ui.Menu) {
						for _, bud := range a.directory.Agents {
							if m.Item(bud.Name).Chosen() {
								v.draft.ConversationID = bud.ConversationID
							}
						}
						for _, group := range a.directory.Groups {
							if m.Item(group.Name).Chosen() {
								v.draft.ConversationID = group.ID
							}
						}
					})
				})
				ui.Text(c, "The chat’s lead Bud owns each investigation.").TextColor(c.Theme().TextMuted)
				a.watchScopeFields(c)
				ui.TextInput(c.Key("watch-name"), &v.draft.Name).Label("Watch name").Placeholder("What are we watching?")
				if a.serverProtocol >= api.ConnectionChatProtocol {
					watchFilterFields(c, &v.draft)
				}
				ui.TextArea(c.Key("watch-instructions"), &v.draft.Instructions).Height(68).
					Label("Standing instructions").Placeholder("What should your Bud look for?")
				ui.TextInput(c.Key("watch-ignore"), &v.draft.Ignore).Label("Ignore matching text").Placeholder("Optional text to ignore")
				ui.Row(c).Gap(7).Wrap().Children(func() {
					modes := []string{tasks.WatchDigest, tasks.WatchInvestigate, tasks.WatchAsk}
					if a.serverProtocol >= api.ConnectionChatProtocol {
						modes = append(modes, tasks.WatchNotify)
					}
					for _, mode := range modes {
						ui.Button(c.Key("watch-mode-"+mode), tasks.WatchModeName(mode)).Selected(v.draft.Mode == mode).
							OnClick(func() { v.draft.Mode = mode })
					}
				})
				modeHelp := "Collect matching events in the inbox."
				switch v.draft.Mode {
				case tasks.WatchInvestigate:
					modeHelp = "Use your standing instructions to investigate matching events and report what the Bud found."
				case tasks.WatchAsk:
					modeHelp = "Ask for approval before each investigation."
				case tasks.WatchNotify:
					modeHelp = "Post matching events in the chat without starting an investigation."
				}
				ui.Text(c, modeHelp).FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, "Minutes per attempt").TextColor(c.Theme().TextMuted)
					ui.TextInput(c.Key("watch-budget"), &v.budget).Label("Investigation budget in minutes").Width(65)
				})
				ui.Row(c).Gap(8).Children(func() {
					ui.Button(c.Key("watch-cancel"), "Cancel").Disabled(v.busy).OnClick(func() { v.draft = tasks.Watch{} })
					ui.PrimaryButton(c.Key("watch-save"), submit).Disabled(v.busy || strings.TrimSpace(v.draft.Name) == "" ||
						strings.TrimSpace(v.draft.Scope) == "").OnClick(func() {
						watch := v.draft
						watch.Filters = slices.Clone(watch.Filters)
						watch.Budget, _ = strconv.Atoi(strings.TrimSpace(v.budget))
						a.changeAutonomy(func(ctx context.Context, client *api.Client) error { return client.CreateWatch(ctx, watch) },
							func() { v.draft = tasks.Watch{} })
					})
				})
			})
		})
	}
	if len(v.watches) == 0 && v.draft.Source == "" {
		controlEmpty(c, clockMark, "No watches yet", "Choose a connection and what Buddy should follow.")
	}
	for _, watch := range v.watches {
		disconnected := watch.ConnectionID != "" && !a.integration(watch.ConnectionID).Connected
		connectionSection(c.Key(watch.ID), func() {
			ui.Row(c).Padding(12).Gap(8).Children(func() {
				detail := a.watchConnectionLabel(watch.Source, watch.ConnectionID) + " · " + watch.Scope + " · " + tasks.WatchModeName(watch.Mode)
				for _, filter := range watch.Filters {
					detail += " · " + watchFilterName(filter.Field) + " " + filter.Value
				}
				if disconnected {
					detail += " · Disconnected"
				}
				connectionDetail(c, clockMark, watch.Name, detail)
				ui.Button(c.Key("watch-edit-"+watch.ID), "Edit").Disabled(v.busy).OnClick(func() {
					v.draft, v.budget = watch, strconv.Itoa(watch.Budget)
					v.draft.Filters = slices.Clone(watch.Filters)
				})
				label := "Pause"
				if !watch.Enabled {
					label = "Resume"
				}
				if !watch.Enabled && a.serverProtocol >= api.StandingRecoveryProtocol {
					ui.Button(c.Key("watch-archive-"+watch.ID), "Archive").Disabled(v.busy).OnClick(func() {
						a.changeAutonomy(func(ctx context.Context, client *api.Client) error { return client.ArchiveWatch(ctx, watch.ID) },
							func() {
								if v.draft.ID == watch.ID {
									v.draft = tasks.Watch{}
								}
							})
					})
				}
				if disconnected {
					ui.Button(c.Key("watch-reconnect-"+watch.ID), "Connections").OnClick(func() { a.showConnectionTab(connectionsIntegrations) })
					return
				}
				ui.Button(c.Key("watch-toggle-"+watch.ID), label).Disabled(v.busy).OnClick(func() {
					a.changeAutonomy(func(ctx context.Context, client *api.Client) error {
						return client.SetWatchEnabled(ctx, watch.ID, !watch.Enabled)
					}, nil)
				})
			})
		})
	}
}

func (a *desktop) watchChatName(id string) string {
	for _, bud := range a.directory.Agents {
		if bud.ConversationID == id {
			return bud.Name
		}
	}
	for _, group := range a.directory.Groups {
		if group.ID == id {
			return group.Name
		}
	}
	return "Current chat"
}

func inboxStatus(item tasks.InboxItem) string {
	switch item.State {
	case "approval":
		return "Approval needed"
	case "pending":
		return "Waiting for a work slot"
	case "queued":
		if item.Attempts != 0 {
			return "Retrying"
		}
		return "Queued"
	case "running":
		return "Investigating"
	case "succeeded":
		return "Report ready"
	case "notified":
		return "Posted in chat"
	case "overflow":
		return "Grouped in chat"
	case "needs_input":
		return "Needs your input"
	case "digest":
		return "Digest"
	default:
		return strings.ReplaceAll(item.State, "_", " ")
	}
}

func (a *desktop) autonomyInbox(c *ui.Context) {
	v := &a.autonomy
	controlSection(c, "Inbox", "Findings, approval requests, and digests from your Buds.", nil)
	if !slices.ContainsFunc(v.inbox, func(item tasks.InboxItem) bool { return item.State != "dismissed" }) {
		title := "You’re all caught up"
		if v.loading {
			title = "Checking the inbox…"
		}
		controlEmpty(c, buddyMark, title, "New findings and requests will appear here.")
	}
	for _, item := range v.inbox {
		if item.State == "dismissed" {
			continue
		}
		connectionSection(c.Key(item.ID), func() {
			ui.Column(c).Padding(12).Gap(7).Children(func() {
				ui.Text(c, item.Signal.Title).FontWeight(680).Selectable()
				detail := item.Watch + " · " + item.Bud + " · " + inboxStatus(item) + " · " + item.At.Local().Format("Jan 2, 15:04")
				if connection := a.integration(item.Signal.ConnectionID); connection.ID != "" {
					detail = connection.Name + " · " + detail
				}
				ui.Text(c, detail).
					FontSize(12).TextColor(c.Theme().TextMuted)
				if item.Result != "" {
					messageMarkdown(c.Key("watch-result"), item.Result)
				}
				ui.Row(c).Gap(7).Children(func() {
					switch {
					case item.State == "approval":
						for _, action := range []struct{ label, key string }{{"Investigate", "approve"}, {"Dismiss", "dismiss"}} {
							ui.Button(c.Key(action.key+"-"+item.ID), action.label).Disabled(v.busy).OnClick(func() {
								a.changeAutonomy(func(ctx context.Context, client *api.Client) error {
									return client.DecideInbox(ctx, item.ID, action.key)
								}, nil)
							})
						}
					case item.State == "notified" || item.State == "overflow":
						ui.Button(c.Key("open-"+item.ID), "Open chat").OnClick(func() { a.openConversation(item.ConversationID) })
					case item.TaskID != "":
						work := tasks.WorkItem{ID: item.TaskID, ConversationID: item.ConversationID, Title: item.Signal.Title, InputID: item.InputID}
						if item.InputID != "" {
							ui.PrimaryButton(c.Key("answer-"+item.ID), "Reply in chat").OnClick(func() { a.askAboutWork(work) })
						}
						ui.Button(c.Key("open-"+item.ID), "View work").OnClick(func() {
							a.openWork(work)
						})
					}
					if item.Signal.URL != "" {
						ui.Button(c.Key("source-"+item.ID), "Open source").OnClick(func() { c.OpenURL(item.Signal.URL) })
					}
					if !item.Seen && item.State != "approval" {
						ui.Button(c.Key("read-"+item.ID), "Mark read").Disabled(v.busy).OnClick(func() {
							a.changeAutonomy(func(ctx context.Context, client *api.Client) error {
								return client.DecideInbox(ctx, item.ID, "read")
							}, nil)
						})
					}
				})
			})
		})
	}
}

func (a *desktop) autonomyMemory(c *ui.Context) {
	v := &a.autonomy
	removed := a.budWasRemoved(v.agent)
	budName := "Choose a Bud"
	for _, bud := range a.directory.Agents {
		if bud.ID == v.agent {
			budName = bud.Name
			break
		}
	}
	controlSection(c, "Bud memory", "Review what a Bud knows and how it should work with you.", func() {
		ui.MenuButton(c.Key("memory-bud-selector"), budName, func(menu *ui.Menu) {
			for _, bud := range a.directory.Agents {
				if menu.Item(bud.Name).Checked(bud.ID == v.agent).Chosen() && v.agent != bud.ID {
					v.selectMemoryBud(bud.ID)
				}
			}
		}).Label("Choose a Bud for memory").Description(budName).Tooltip(budName).MaxWidth(200).MinWidth(0).Disabled(v.busy || len(a.directory.Agents) == 0)
	})
	if removed {
		ui.Text(c, "This Bud was removed. Copy your draft before choosing another Bud.").TextColor(c.Theme().TextMuted)
	}
	if a.serverProtocol >= api.ConversationHistoryProtocol {
		a.memoryDocumentTabs(c, removed)
		if v.memoryPanel != "" && v.memoryPanel != "Saved notes" {
			return
		}
	}
	controlSection(c, "Saved notes", "Notes you’ve added for this Bud to use in chat.", func() {
		if !v.editingMemory {
			ui.Button(c.Key("new-memory"), "Add note").Disabled(removed || v.busy || v.agent == "").
				OnClick(func() { v.editingMemory = true })
		}
	})
	if v.editingMemory {
		a.memoryNoteEditor(c, removed)
	} else if len(v.memories) == 0 && !v.loading {
		controlEmpty(c, fileMark, "No saved notes", "Add a preference, a useful detail, or a reminder for this Bud.")
	}
	for _, note := range v.memories {
		connectionSection(c.Key(note.ID), func() {
			ui.Row(c).FillWidth().MinWidth(0).Padding(12).Gap(9).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, note.Note).FontWeight(620).Selectable()
					if note.Source != "" {
						ui.Text(c, note.Source).FontSize(12).TextColor(c.Theme().TextMuted)
					}
				})
				quietControl(c.Key("memory-edit-"+note.ID), "Edit", nil, !removed && !v.busy, func() {
					v.memory, v.expiry, v.editingMemory = note, "", true
					if !note.Expires.IsZero() {
						v.expiry = note.Expires.Local().Format("2006-01-02")
					}
				})
				quietControl(c.Key("memory-remove-"+note.ID), "Remove", nil, !v.busy, func() {
					a.changeAutonomy(func(ctx context.Context, client *api.Client) error { return client.DeleteMemory(ctx, note.ID) },
						func() {
							if v.memory.ID == note.ID {
								v.memory, v.expiry, v.editingMemory = tasks.MemoryNote{}, "", false
							}
						})
				})
			})
		})
	}
}

func (a *desktop) memoryNoteEditor(c *ui.Context, removed bool) {
	v := &a.autonomy
	connectionSection(c, func() {
		ui.Column(c).FillWidth().Padding(13).Gap(9).Children(func() {
			sectionLabel(c, "Note")
			ui.TextArea(c.Key("memory-note"), &v.memory.Note).Height(74).Label("Memory note").
				Placeholder("Something this Bud should remember")
			ui.Row(c).FillWidth().Gap(10).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					sectionLabel(c, "Source · optional")
					ui.TextInput(c.Key("memory-source"), &v.memory.Source).Label("Memory source").Placeholder("Where this came from")
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					sectionLabel(c, "Expiry · optional")
					ui.TextInput(c.Key("memory-expiry"), &v.expiry).Label("Memory expiry").Placeholder("YYYY-MM-DD")
				})
			})
			ui.Row(c).FillWidth().Gap(7).Justify(ui.End).Children(func() {
				cancel := func() { v.memory, v.expiry, v.editingMemory = tasks.MemoryNote{}, "", false }
				quietControl(c.Key("memory-cancel"), "Cancel", nil, !v.busy, cancel)
				ui.PrimaryButton(c.Key("memory-save"), "Save note").
					Disabled(v.busy || removed || v.agent == "" || strings.TrimSpace(v.memory.Note) == "").OnClick(func() {
					note := v.memory
					note.AgentID = v.agent
					note.Expires = time.Time{}
					if date := strings.TrimSpace(v.expiry); date != "" {
						expires, err := time.ParseInLocation("2006-01-02", date, time.Local)
						if err != nil {
							v.err = "Use YYYY-MM-DD for the expiry date."
							return
						}
						note.Expires = expires
					}
					a.changeAutonomy(func(ctx context.Context, client *api.Client) error { return client.SaveMemory(ctx, note) }, cancel)
				})
			})
		})
	})
}

func (a *desktop) autonomyHealth(c *ui.Context) {
	h := a.autonomy.health
	controlSection(c, "Server health", "Queue activity, connected workers, and delivery status.", nil)
	if h.StartedAt.IsZero() {
		ui.Text(c, "Checking the home server…").TextColor(c.Theme().TextMuted)
		return
	}
	autonomyHealthRow(c, "Home server", "Started "+h.StartedAt.Local().Format("Jan 2, 15:04"))
	autonomyHealthRow(c, "Queue", fmt.Sprintf("%d queued · %d running · %d needs input · %d over budget", h.Queued, h.Running, h.NeedsInput, h.Stuck))
	autonomyHealthRow(c, "Routines", fmt.Sprintf("%d overdue", h.Overdue))
	if h.GitHub == "webhook" {
		autonomyHealthRow(c, "GitHub", "Live webhooks only. Offline deliveries need to be redelivered.")
	}
	if h.Exhausted != 0 {
		connectionSection(c, func() {
			ui.Row(c).Padding(12).Gap(8).Children(func() {
				connectionDetail(c, clockMark, "Notifications", fmt.Sprintf("%d deliveries need a retry", h.Exhausted))
				ui.Button(c.Key("retry-notifications"), "Retry now").Disabled(a.autonomy.busy).OnClick(func() {
					a.changeAutonomy(func(ctx context.Context, client *api.Client) error { return client.RetryStandingNotifications(ctx) }, nil)
				})
			})
		})
	}
	for _, worker := range h.Workers {
		status := "Offline"
		if worker.Online {
			status = "Online"
		}
		autonomyHealthRow(c, worker.Name, status)
	}
	for _, feed := range h.Feeds {
		status := "Waiting for the first check or signed event"
		if feed.Problem != "" {
			status = feed.Problem
		} else if !feed.Success.IsZero() {
			status = "Last success " + feed.Success.Local().Format("Jan 2, 15:04")
		}
		name := feed.Scope
		if feed.ConnectionID != "" {
			name = a.watchConnectionLabel(feed.Source, feed.ConnectionID) + " · " + name
		}
		autonomyHealthRow(c, name, status)
	}
}

func autonomyHealthRow(c *ui.Context, name, description string) {
	connectionSection(c, func() {
		ui.Row(c).Padding(12).Gap(8).Children(func() { connectionDetail(c, hostMark, name, description) })
	})
}
