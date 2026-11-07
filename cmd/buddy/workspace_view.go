package main

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

var (
	groupMark   = svg(`<circle cx="9" cy="8" r="3"/><path d="M3 19v-1a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v1m2-14a3 3 0 0 1 0 6m4 8v-1a4 4 0 0 0-3-3.9"/>`)
	plusMark    = svg(`<path d="M12 5v14M5 12h14"/>`)
	detailsMark = svg(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M14 4v16"/>`)
	fileMark    = svg(`<path d="M6 3h8l4 4v14H6zm8 0v5h4"/>`)
	clockMark   = svg(`<circle cx="12" cy="12" r="8"/><path d="M12 7v5l3 2"/>`)
	moreMark    = svg(`<path d="M5 12h.01m7 0h.01m7 0h.01" stroke-width="2.8"/>`)
	arrowMark   = svg(`<path d="M9 6l6 6-6 6"/>`)
)

func smallIcon(c *ui.Context, label string, mark *ui.SVG, enabled bool, action func()) {
	btn := ui.ButtonBase(c).Label(label).Tooltip(label).Disabled(!enabled).
		Size(26, 26).Shrink(0).Center().Radius(6).OnClick(action)
	if btn.Hovered() {
		btn.Background(c.Theme().SurfacePressed)
	}
	btn.Children(func() { ui.Icon(c, mark).Size(15, 15).TextColor(c.Theme().TextMuted) })
}

func (a *desktop) chatNav(c *ui.Context, id, title string, icon *ui.SVG, unread bool) {
	selected := id == a.currentConversation() && (a.section == "" || a.section == "routine")
	label := unreadChatLabel(title, unread)
	btn := ui.ButtonBase(c.Key("chat-"+id)).Label(label).Tooltip(label).FillWidth().Height(32).
		Gap(8).Padding(0, 8).Radius(7).Justify(ui.Start).OnClick(func() { a.openConversation(id) })
	if a.serverProtocol >= api.ConversationHistoryProtocol {
		btn.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("Open chat").Chosen() {
				a.openConversation(id)
			}
			if menu.Item("Call history…").Disabled(!a.online || a.history.busy).Chosen() {
				a.openHistory(id)
			}
			menu.Separator()
			if menu.Item("Archive group chat").Disabled(!a.canChangeHistory()).Chosen() {
				a.changeHistory(id, "", true)
			}
		})
	}
	if selected {
		btn.Background(c.Theme().SurfacePressed)
	} else if btn.Hovered() {
		btn.Background(c.Theme().Surface)
	}
	btn.Children(func() {
		ui.Icon(c, icon).Size(15, 15).Shrink(0).TextColor(c.Theme().TextMuted)
		ui.Text(c, title).Grow(1).SingleLine().MinWidth(0).FontSize(13).FontWeight(600)
		if unread {
			sidebarUnreadDot(c)
		}
	})
}

func (a *desktop) budNav(c *ui.Context, bud tasks.AgentRef) {
	role := budRole(bud.Role)
	selected := bud.ConversationID == a.currentConversation() && (a.section == "" || a.section == "routine")
	primary := bud.ID == a.directory.PrimaryAgentID
	unread := a.chatHasUnread(bud.ConversationID, bud.HasNewResult, bud.UnreadThrough)
	detail := bud.Name + " · " + role
	if primary {
		detail += " · Primary Bud"
	}
	if unread {
		detail += " · Unread activity"
	}
	btn := ui.ButtonBase(c.Key("chat-"+bud.ConversationID)).Label(unreadChatLabel(bud.Name, unread)).Description(role).Tooltip(detail).
		FillWidth().Height(43).Padding(0, 8).Radius(7).Justify(ui.Start).
		OnClick(func() { a.openConversation(bud.ConversationID) })
	if selected {
		btn.Background(c.Theme().SurfacePressed)
	} else if btn.Hovered() {
		btn.Background(c.Theme().Surface)
	}
	btn.Children(func() {
		ui.Row(c).FillWidth().Gap(7).AlignItems(ui.Center).Children(func() {
			a.drawProfile(c, a.directoryProfile(bud), 27, false, orbIdle)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, bud.Name).FontSize(13).FontWeight(650).MinWidth(0).SingleLine()
					if primary {
						ui.Icon(c, primaryMark).Size(12, 12).Shrink(0).TextColor(c.Theme().Success)
					}
				})
				ui.Text(c, role).FontSize(10).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if unread {
				sidebarUnreadDot(c)
			}
		})
	})
}

func unreadChatLabel(name string, unread bool) string {
	if unread {
		return name + ", unread activity"
	}
	return name
}

func sidebarUnreadDot(c *ui.Context) {
	ui.Box(c).Size(7, 7).Shrink(0).Radius(4).Background(c.Theme().Accent)
}

func sidebarHeading(c *ui.Context, title, action string, enabled bool, add func()) {
	ui.Row(c).Height(28).Padding(0, 4, 0, 8).Children(func() {
		ui.Text(c, title).Grow(1).FontSize(11).FontWeight(680).TextColor(c.Theme().TextMuted)
		smallIcon(c.Key(action), action, plusMark, enabled, add)
	})
}

func (a *desktop) inspector(c *ui.Context, header, inset float32) {
	profile := a.inspectorAgent()
	ui.Column(c).Width(234).Shrink(0).MinHeight(0).Background(c.Theme().SurfaceHover).
		BorderWidth(0, 0, 0, 1).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Height(header).Shrink(0).FillWidth().Padding(0, max(14, inset+8), 0, 14).
			BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).DragWindow().Children(func() {
			ui.Text(c, "Workspace").Role(ui.RoleHeading).Grow(1).MinWidth(0).SingleLine().FontSize(13).FontWeight(680)
		})
		ui.Scroll(c.Key("agent-inspector")).Grow(1).MinHeight(0).FillWidth().Padding(13, 14, 18).Gap(13).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					a.drawProfile(c, profile, 28, false, orbIdle)
					ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
						name := profile.Name
						if name == "" {
							name = "Buddy"
						}
						ui.Text(c, name).SingleLine().FontSize(13).FontWeight(650)
						ui.Text(c, budRole(profile.Role)).SingleLine().FontSize(11).TextColor(c.Theme().TextMuted)
					})
					if len(a.chat.Members) > 1 {
						ui.Button(c.Key("inspector-agent"), "⋯").Label("Choose Bud workspace").Size(25, 24).
							PopoverMenuButton(func(menu *ui.Menu) {
								for _, member := range a.chat.Members {
									if menu.Item(member.Name).Checked(member.ID == profile.ID).Chosen() {
										a.inspectAgent(member.ID)
									}
								}
							})
					}
				})
			})
			a.workspaceFiles(c, profile)
			a.backgroundWork(c)
			a.scheduledRoutines(c)
			if a.inspectorErr != "" {
				ui.Text(c, a.inspectorErr).FontSize(11).TextColor(c.Theme().Danger)
			}
		})
	})
}

func (a *desktop) toggleInspector() {
	a.inspectorClosed = !a.inspectorClosed
	a.refreshSoon()
}

func inspectorHeading(c *ui.Context, title string, mark *ui.SVG) {
	ui.Icon(c, mark).Size(14, 14).Shrink(0).TextColor(c.Theme().TextMuted)
	ui.Text(c, title).Grow(1).FontSize(12).FontWeight(680)
}

func (a *desktop) workspaceFiles(c *ui.Context, profile tasks.Agent) {
	path := profile.Workspace
	canBrowse := a.canBrowseWorkspace(profile)
	ui.Column(c).Gap(5).Padding(12, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Gap(6).Height(25).Children(func() {
			inspectorHeading(c, "Files", repoMark)
			smallIcon(c.Key("open-workspace"), "Browse workspace files", arrowMark, canBrowse, func() {
				a.openWorkspaceFile(profile, ".")
			})
		})
		if path != "" {
			ui.Text(c, displayWorkspace(path, profile.ID)).Tooltip(path).SingleLine().FontSize(11).TextColor(c.Theme().TextMuted)
		}
		switch {
		case a.workspaceErr != "":
			ui.Text(c, a.workspaceErr).FontSize(11).TextColor(c.Theme().TextMuted)
		case a.online && a.serverProtocol != 0 && a.serverProtocol < api.WorkspaceFilesProtocol:
			ui.Text(c, "Update the server to open files in Buddy.").FontSize(11).TextColor(c.Theme().TextMuted)
		case a.workspace.Path != path || path == "":
			ui.Text(c, "Loading workspace…").FontSize(11).TextColor(c.Theme().TextMuted)
		case len(a.workspace.Entries) == 0:
			ui.Text(c, "No files yet.").FontSize(11).TextColor(c.Theme().TextMuted)
		default:
			for _, entry := range a.workspace.Entries {
				mark := fileMark
				if entry.Folder {
					mark = repoMark
				}
				btn := ui.ButtonBase(c.Key("file-"+entry.Name)).Label(entry.Name).Tooltip("Open in Buddy").
					Height(25).FillWidth().Justify(ui.Start).Gap(6).Radius(5).Padding(0, 3).
					Disabled(!canBrowse).OnClick(func() { a.openWorkspaceFile(profile, entry.Name) })
				if btn.Hovered() {
					btn.Background(c.Theme().SurfacePressed)
				}
				btn.Children(func() {
					ui.Icon(c, mark).Size(13, 13).Shrink(0).TextColor(c.Theme().TextMuted)
					ui.Text(c, entry.Name).SingleLine().MinWidth(0).FontSize(12)
				})
			}
		}
	})
}

func displayWorkspace(path, id string) string {
	if filepath.Base(path) == id {
		return "Bud workspace"
	}
	return filepath.Base(path)
}

func (a *desktop) backgroundWork(c *ui.Context) {
	ui.Column(c).Gap(8).Padding(12, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Gap(6).Height(22).Children(func() {
			inspectorHeading(c, "Background work", workMark)
			smallIcon(c.Key("work-overview"), "See all jobs in this chat", arrowMark, a.online, func() {
				a.openWorkInChat(tasks.WorkItem{}, a.currentConversation())
			})
		})
		if len(a.chat.Work) == 0 {
			ui.Text(c, "No background tasks yet.").FontSize(11).TextColor(c.Theme().TextMuted)
		}
		for _, work := range a.chat.Work[:min(3, len(a.chat.Work))] {
			ui.Column(c.Key("work-" + work.ID)).Gap(3).Children(func() {
				ui.Row(c).Gap(5).Children(func() {
					ui.ButtonBase(c.Key("open-work-" + work.ID)).Label("Open " + work.Title).Grow(1).MinWidth(0).Justify(ui.Start).
						Tooltip("See progress and result").OnClick(func() {
						work.ConversationID = a.currentConversation()
						a.openWorkInChat(work, work.ConversationID)
					}).Children(func() {
						ui.Text(c, work.Title).Grow(1).MinWidth(0).FontWeight(620).FontSize(12).SingleLine()
						ui.Icon(c, arrowMark).Size(12, 12).Shrink(0).TextColor(c.Theme().TextMuted)
					})
					if work.Status == tasks.Running || work.Status == tasks.Queued || work.Status == tasks.NeedsInput {
						smallIcon(c.Key("stop-work-"+work.ID), "Stop "+work.Title, stopMark, a.online && a.stopping == "",
							func() { a.cancelTurn(work.ID, false) })
					}
				})
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					jobIndicator(c, work.Status)
					ui.Text(c, workStatus(work.Status)).FontSize(11).TextColor(c.Theme().TextMuted)
				})
			})
		}
		if len(a.chat.Work) > 3 {
			ui.Button(c.Key("more-chat-jobs"), "See all jobs in this chat").AlignSelf(ui.Start).Height(25).
				Disabled(!a.online).OnClick(func() { a.openWorkInChat(tasks.WorkItem{}, a.currentConversation()) })
		}
	})
}

func workActivity(work tasks.WorkItem) string {
	if work.Status == tasks.Queued && (work.Activity == tasks.DeviceAttachmentUpgrade ||
		work.Activity == tasks.WorkspaceReviewMessage) {
		return work.Activity
	}
	if work.Status != tasks.Running {
		return ""
	}
	if work.Update != "" {
		return work.Update
	}
	switch work.Kind {
	case tasks.Reading, tasks.Searching, tasks.Editing, tasks.RunningTool:
		return work.Activity
	default:
		return ""
	}
}

func workStatus(status tasks.Status) string {
	switch status {
	case tasks.Queued:
		return "Queued"
	case tasks.Running:
		return "In progress"
	case tasks.NeedsInput:
		return "Waiting for your reply"
	case tasks.Cancelling:
		return "Stopping…"
	case tasks.Succeeded:
		return "Finished"
	case tasks.Cancelled:
		return "Stopped"
	case tasks.Interrupted:
		return "Interrupted"
	default:
		return "Needs attention"
	}
}

func (a *desktop) scheduledRoutines(c *ui.Context) {
	ui.Column(c).Gap(8).Padding(12, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Gap(6).Height(25).Children(func() {
			inspectorHeading(c, "Routines", clockMark)
			smallIcon(c.Key("new-routine"), "New routine", plusMark, a.online && a.chat.ID != "" && !a.chat.Archived && !a.chat.Agent.Removed, a.startRoutine)
		})
		if len(a.chat.Routines) == 0 {
			ui.Text(c, "No routines yet. Ask in chat or add one here.").FontSize(11).TextColor(c.Theme().TextMuted)
		}
		for _, routine := range a.chat.Routines {
			removed := a.budWasRemoved(routine.AgentID)
			ui.Row(c.Key("routine-" + routine.ID)).Gap(6).Children(func() {
				ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, routine.Name).SingleLine().FontSize(12).FontWeight(620).Tooltip(routine.Prompt)
					status := "Paused"
					if removed {
						status = "Bud removed"
					} else if routine.DeviceDisconnected {
						status = "Disconnected"
					} else if routine.Enabled {
						status = "Next " + routineDate(routine.NextAt, c.Now())
					}
					if routine.Device != "" {
						status = routine.Device + " · " + status
					}
					ui.Text(c, tasks.RoutineCadence(routine.Minutes)+" · "+status).
						FontSize(11).TextColor(c.Theme().TextMuted)
					if routine.DeviceDisconnected && !removed {
						ui.Text(c, "Create a new routine on an available device.").FontSize(11).TextColor(c.Theme().TextMuted)
					}
				})
				button := ui.ButtonBase(c.Key("routine-menu-"+routine.ID)).Label("Manage "+routine.Name).Size(26, 26).
					Radius(6).Center().Shrink(0).Disabled(!a.online)
				if button.Hovered() {
					button.Background(c.Theme().SurfacePressed)
				}
				button.Children(func() { ui.Icon(c, moreMark).Size(14, 14).TextColor(c.Theme().TextMuted) })
				button.PopoverMenuButton(func(menu *ui.Menu) {
					if !routine.DeviceDisconnected && !removed {
						label := "Resume"
						if routine.Enabled {
							label = "Pause"
						}
						if menu.Item(label).Chosen() {
							enabled := !routine.Enabled
							a.changeRoutine(routine.ID, &enabled)
						}
					}
					if menu.Item("Delete routine").Chosen() {
						a.changeRoutine(routine.ID, nil)
					}
				})
			})
		}
	})
}

func (a *desktop) groupSettings(c *ui.Context) {
	controlPage(c.Key("group-settings"), "New group chat", "Choose the Buds who can share this conversation and hand off work.", nil, func() {
		ui.Column(c).FillWidth().Gap(9).Children(func() {
			ui.TextInput(c.Key("group-name"), &a.group.Name).Label("Group name").Placeholder("Group name").Height(38)
			for _, bud := range a.directory.Agents {
				selected := slices.Contains(a.group.Members, bud.ID)
				if ui.Checkbox(c.Key("member-"+bud.ID), &selected, bud.Name+" · "+budRole(bud.Role)).Changed() {
					if selected {
						a.group.Members = append(a.group.Members, bud.ID)
					} else {
						a.group.Members = slices.DeleteFunc(a.group.Members, func(id string) bool { return id == bud.ID })
						if a.group.LeadAgentID == bud.ID {
							a.group.LeadAgentID = ""
						}
					}
				}
			}
			if lead := a.draftGroupLead(); lead != "" {
				members := make([]tasks.AgentRef, 0, len(a.group.Members))
				for _, bud := range a.directory.Agents {
					if slices.Contains(a.group.Members, bud.ID) {
						members = append(members, bud)
					}
				}
				ui.Row(c).Gap(9).Children(func() {
					ui.Text(c, "Lead Bud").FontWeight(650)
					a.groupLeadPicker(c.Key("new-group-lead"), a.directoryAgentName(lead), lead, members,
						a.canSetLeadership() && len(members) > 1, func(id string) { a.group.LeadAgentID = id })
				})
				ui.Text(c, "The lead is the default for replies and calls.").FontSize(11).TextColor(c.Theme().TextMuted)
			}
		})
		a.formActions(c, "Create group", len(a.group.Members) >= 2 && len(a.group.Members) <= 8 && strings.TrimSpace(a.group.Name) != "", a.createGroup)
	})
}

func (a *desktop) directoryAgentName(id string) string {
	for _, agent := range a.directory.Agents {
		if agent.ID == id {
			return agent.Name
		}
	}
	return "Bud"
}

func (a *desktop) groupLeadPicker(c *ui.Context, label, lead string, members []tasks.AgentRef, enabled bool, choose func(string)) {
	ui.Button(c, label+" ▾").Label("Choose Lead Bud").Tooltip("Choose the default Bud for replies and calls").
		Height(27).MaxWidth(170).Shrink(0).Disabled(!enabled).PopoverMenuButton(func(menu *ui.Menu) {
		for _, bud := range members {
			if menu.Item(bud.Name + " · " + budRole(bud.Role)).Checked(bud.ID == lead).Chosen() {
				choose(bud.ID)
			}
		}
	})
}

func (a *desktop) routineSettings(c *ui.Context) {
	subtitle := "Runs with " + a.inspectorAgent().Name + " and posts results in this chat."
	controlPage(c.Key("routine-settings"), "New routine", subtitle, nil, func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "Name").FontWeight(650)
			ui.TextInput(c.Key("routine-name"), &a.routine.Name).Label("Routine name").
				Placeholder("e.g. Review open issues").Height(36)
		})
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "Instructions").FontWeight(650)
			ui.TextArea(c.Key("routine-prompt"), &a.routine.Prompt).Label("Routine instructions").
				Placeholder("What should this Bud do each time?").Height(94)
		})
		minutes, valid := a.routineMinutes()
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "Repeat").FontWeight(650)
			a.cadencePicker(c)
			if a.routineCadence == 0 {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, "Every").FontSize(12).TextColor(c.Theme().TextMuted)
					ui.TextInput(c.Key("routine-interval"), &a.routineInterval).Label("Interval in minutes").
						Size(76, 32)
					ui.Text(c, "minutes").FontSize(12).TextColor(c.Theme().TextMuted)
				})
			}
			hint := "Enter 1 to 10,080 minutes."
			if valid {
				hint = "First run " + routineDate(c.Now().Add(time.Duration(minutes)*time.Minute), c.Now())
			}
			ui.Text(c, hint).FontSize(11).TextColor(c.Theme().TextMuted)
		})
		a.formActions(c, "Schedule routine", valid && strings.TrimSpace(a.routine.Name) != "" &&
			strings.TrimSpace(a.routine.Prompt) != "", a.createRoutine)
	})
}

func (a *desktop) cadencePicker(c *ui.Context) {
	ui.Row(c).Gap(4).Padding(3).Background(c.Theme().SurfaceHover).Radius(8).AlignSelf(ui.Start).Children(func() {
		for _, option := range []struct {
			minutes int
			label   string
		}{{60, "Hourly"}, {1440, "Daily"}, {10080, "Weekly"}, {0, "Custom"}} {
			button := ui.RadioBase(c.Key("cadence-"+option.label), &a.routineCadence, option.minutes).
				Label(option.label).Height(28).Padding(0, 11).Radius(6).Center()
			selected := a.routineCadence == option.minutes
			if selected {
				button.Background(c.Theme().Surface).Border(1, c.Theme().Border)
			} else if button.Hovered() {
				button.Background(c.Theme().SurfacePressed)
			}
			button.Children(func() {
				color := c.Theme().TextMuted
				if selected {
					color = c.Theme().Text
				}
				ui.Text(c, option.label).FontSize(12).FontWeight(620).TextColor(color)
			})
		}
	})
}

func routineDate(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	switch at.Format(time.DateOnly) {
	case now.Format(time.DateOnly):
		return "today at " + at.Format("15:04")
	case now.AddDate(0, 0, 1).Format(time.DateOnly):
		return "tomorrow at " + at.Format("15:04")
	default:
		return at.Format("Jan 2 at 15:04")
	}
}

func (a *desktop) formActions(c *ui.Context, label string, valid bool, save func()) {
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, a.editErr).Grow(1).TextColor(c.Theme().Danger).FontSize(12)
		ui.Button(c.Key("form-cancel"), "Cancel").Disabled(a.saving).OnClick(func() { a.section = "" })
		ui.PrimaryButton(c.Key("form-save"), label).Disabled(a.saving || !valid || !a.online).OnClick(save)
	})
}

func (a *desktop) drawProfile(c *ui.Context, profile tasks.Agent, size float32, live bool, mood orbMood) {
	cache := &a.profileAvatar
	if profile.ID != "" && profile.ID != a.chat.Agent.ID {
		if a.otherAvatars == nil {
			a.otherAvatars = make(map[string]*avatarCache)
		}
		cache = a.otherAvatars[profile.ID]
		if cache == nil {
			cache = &avatarCache{}
			a.otherAvatars[profile.ID] = cache
		}
	}
	look := avatarAppearance(profile)
	avatar(c, size, look, cache.image(look.Image), live, mood)
}

func notificationLabel(message tasks.Message, author string) string {
	label := "Routine result"
	if author != "" {
		label = author + " · " + label
	}
	if !message.At.IsZero() {
		label += " · " + message.At.Local().Format(time.Kitchen)
	}
	return label
}
