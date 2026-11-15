package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

var (
	groupMark    = svg(`<circle cx="9" cy="8" r="3"/><path d="M3 19v-1a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v1m2-14a3 3 0 0 1 0 6m4 8v-1a4 4 0 0 0-3-3.9"/>`)
	plusMark     = svg(`<path d="M12 5v14M5 12h14"/>`)
	sidebarMark  = svg(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/>`)
	waveformMark = svg(`<path d="M4 10v4m4-7v10m4-13v16m4-13v10m4-7v4"/>`)
	fileMark     = svg(`<path d="M6 3h8l4 4v14H6zm8 0v5h4"/>`)
	clockMark    = svg(`<circle cx="12" cy="12" r="8"/><path d="M12 7v5l3 2"/>`)
	moreMark     = svg(`<path d="M5 12h.01m7 0h.01m7 0h.01" stroke-width="2.8"/>`)
	arrowMark    = svg(`<path d="M9 6l6 6-6 6"/>`)
	primaryMark  = svg(`<path d="m12 3 2.5 5.6 6 .6-4.5 4 1.3 6-5.3-3.1-5.3 3.1 1.3-6-4.5-4 6-.6L12 3Z" fill="currentColor" stroke="none"/>`)
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
	btn := ui.ButtonBase(c.Key("chat-"+id)).Label(label).Tooltip(label).Selected(selected).FillWidth().Height(33).
		Gap(8).Padding(0, 8).Radius(10).Justify(ui.Start).OnClick(func() { a.openConversation(id) })
	if a.compactSidebar {
		btn.Justify(ui.Center)
	}
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
		if !a.compactSidebar {
			ui.Text(c, title).Grow(1).SingleLine().MinWidth(0).FontSize(13).FontWeight(530).TextColor(c.Theme().Text)
		}
		if unread {
			sidebarUnreadDot(c)
		}
	})
}

func (a *desktop) budNav(c *ui.Context, bud tasks.AgentRef) {
	selected := bud.ConversationID == a.currentConversation() && (a.section == "" || a.section == "routine")
	primary := bud.ID == a.directory.PrimaryAgentID
	role := strings.TrimSpace(bud.Role)
	unread := a.chatHasUnread(bud.ConversationID, bud.HasNewResult, bud.UnreadThrough)
	label := unreadChatLabel(bud.Name, unread)
	detail := role
	if primary {
		detail = "Primary Bud"
		if role != "" {
			detail += " · " + role
		}
	}
	tooltip := label
	if detail != "" {
		tooltip += " · " + detail
	}
	btn := ui.ButtonBase(c.Key("chat-"+bud.ConversationID)).Label(label).Description(detail).Tooltip(tooltip).Selected(selected).
		FillWidth().Height(43).Padding(0, 8).Gap(7).Radius(10).Justify(ui.Start).
		OnClick(func() { a.openConversation(bud.ConversationID) })
	if a.compactSidebar {
		btn.Height(45).Justify(ui.Center)
	}
	if selected {
		btn.Background(c.Theme().SurfacePressed)
	} else if btn.Hovered() {
		btn.Background(c.Theme().Surface)
	}
	working := a.chat.Active != nil && (a.chat.Active.Status == tasks.Running || a.chat.Active.Status == tasks.Cancelling) &&
		(a.chat.Active.AgentID == bud.ID || (a.chat.Active.AgentID == "" && selected))
	mood := orbIdle
	if working {
		mood = orbSidebarActive
	}
	live := primary || selected || working || btn.Hovered()
	profile := a.directoryProfile(bud)
	btn.Children(func() {
		if a.compactSidebar {
			ui.Box(c).Size(27, 27).Shrink(0).Center().Children(func() {
				a.drawProfile(c, profile, 27, live, mood)
				if primary {
					ui.Icon(c, primaryMark).Size(10, 10).Absolute().Bottom(-1).Right(-2).TextColor(c.Theme().Accent)
				}
			})
		} else {
			a.drawProfile(c, profile, 27, live, mood)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Row(c).Gap(4).Children(func() {
					ui.Text(c, bud.Name).FontSize(13).FontWeight(570).MinWidth(0).SingleLine().TextColor(c.Theme().Text)
					if primary {
						ui.Icon(c, primaryMark).Size(12, 12).Shrink(0).TextColor(c.Theme().Accent)
					}
				})
				if role != "" {
					ui.Text(c, role).MinWidth(0).SingleLine().FontSize(11).TextColor(c.Theme().TextMuted)
				}
			})
		}
		if unread {
			sidebarUnreadDot(c)
		}
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
	name := profile.Name
	if name == "" {
		name = "Buddy"
	}
	ui.Column(c).Width(272).Shrink(0).MinHeight(0).Background(c.Theme().Background).
		BorderWidth(0, 0, 0, 1).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Height(header).Shrink(0).FillWidth().Padding(0, max(12, inset+8), 0, 12).
			Gap(5).DragWindow().Children(func() {
			ui.Box(c).Grow(1)
			if len(a.chat.Members) > 1 {
				ui.ButtonBase(c.Key("inspector-agent")).Label("Choose Bud workspace").Size(26, 26).Center().Radius(13).
					PopoverMenuButton(func(menu *ui.Menu) {
						for _, member := range a.chat.Members {
							if menu.Item(member.Name).Checked(member.ID == profile.ID).Chosen() {
								a.inspectAgent(member.ID)
							}
						}
					}).Children(func() { ui.Icon(c, moreMark).Size(15, 15).TextColor(c.Theme().TextMuted) })
			}
			smallIcon(c.Key("close-details"), "Close conversation details", closeMark, true, a.toggleInspector)
		})
		ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(9).Padding(0, 12, 14).Children(func() {
			mood := orbIdle
			if active := a.chat.Active; active != nil && (active.AgentID == "" || active.AgentID == profile.ID) {
				mood = orbMood(active.Kind)
			}
			a.drawProfile(c, profile, 86, true, mood)
			ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(3).Children(func() {
				ui.Row(c).Gap(5).MinWidth(0).Children(func() {
					ui.Text(c, name).SingleLine().MinWidth(0).FontSize(19).FontWeight(620).Role(ui.RoleHeading)
					if profile.ID != "" && profile.ID == a.directory.PrimaryAgentID {
						ui.Icon(c, primaryMark).Size(14, 14).Shrink(0).Label("Primary Bud").TextColor(c.Theme().Accent)
					}
				})
				if role := strings.TrimSpace(profile.Role); role != "" {
					ui.Text(c, role).TextAlign(ui.Center).FontSize(12).TextColor(c.Theme().TextMuted)
				}
			})
			if a.chat.Name == "" && !profile.Removed {
				quietControl(c.Key("edit-chat-bud"), "Edit", nil, a.online, func() { a.showSettingsFor(profile) })
			}
			ui.Row(c).Gap(4).Children(func() {
				inspectorTab(c.Key("inspector-activity"), "Activity", !a.inspectorLibrary, func() { a.inspectorLibrary = false })
				inspectorTab(c.Key("inspector-library"), "Library", a.inspectorLibrary, func() { a.inspectorLibrary = true; a.refreshSoon() })
			})
		})
		ui.Scroll(c.Key("agent-inspector")).Grow(1).MinHeight(0).FillWidth().Padding(0, 14, 18).Gap(16).Children(func() {
			panel := ui.Column(c.Key(a.inspectorLibrary)).FillWidth().Gap(12)
			revealPanel(c, panel)
			panel.Children(func() {
				if a.inspectorLibrary {
					a.workspaceFiles(c, profile)
				} else {
					a.scheduledRoutines(c)
					a.backgroundWork(c)
				}
			})
			if a.inspectorErr != "" {
				ui.Text(c, a.inspectorErr).FontSize(11).TextColor(c.Theme().Danger)
			}
		})
	})
}

func inspectorTab(c *ui.Context, label string, selected bool, action func()) {
	t := c.Theme()
	button := ui.ButtonBase(c).Height(28).Padding(0, 12).Radius(14).
		Label(label).Selected(selected).OnClick(action)
	color := t.TextMuted
	if selected || button.Hovered() {
		button.Background(t.SurfaceHover)
		color = t.Text
	}
	button.Children(func() { ui.Text(c, label).FontSize(12).TextColor(color) })
}

func (a *desktop) toggleInspector() {
	a.inspectorClosed = !a.inspectorClosed
	a.refreshSoon()
}

func inspectorHeading(c *ui.Context, title string, mark *ui.SVG) {
	ui.Icon(c, mark).Size(14, 14).Shrink(0).TextColor(c.Theme().TextMuted)
	ui.Text(c, title).Grow(1).FontSize(12).FontWeight(560).TextColor(c.Theme().TextMuted)
}

func (a *desktop) workspaceFiles(c *ui.Context, profile tasks.Agent) {
	path := profile.Workspace
	canBrowse := a.canBrowseWorkspace(profile)
	ui.Column(c).Gap(5).Padding(8, 10).Radius(13).Background(c.Theme().Surface).Children(func() {
		ui.Row(c).Gap(6).Height(25).Children(func() {
			inspectorHeading(c, "Files", repoMark)
			smallIcon(c.Key("open-workspace"), "Browse workspace files", arrowMark, canBrowse, func() {
				a.openWorkspaceFile(profile, ".")
			})
		})
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

func (a *desktop) backgroundWork(c *ui.Context) {
	items := a.chat.Work
	var active []tasks.WorkItem
	for _, item := range items {
		switch item.Status {
		case tasks.Running, tasks.Queued, tasks.NeedsInput, tasks.Cancelling:
			active = append(active, item)
		}
	}
	status := "Idle"
	if len(active) != 0 {
		status = fmt.Sprintf("%d active", len(active))
	}
	ui.Column(c).Gap(5).Padding(8, 10).Radius(13).Background(c.Theme().Surface).Children(func() {
		button := ui.ButtonBase(c.Key("work-overview")).Label("Work runs").FillWidth().Height(34).
			Radius(8).Gap(7).Padding(0, 4).Disabled(!a.online).OnClick(func() {
			a.openWorkInChat(tasks.WorkItem{}, a.currentConversation())
		})
		if button.Hovered() {
			button.Background(c.Theme().SurfaceHover)
		}
		button.Children(func() {
			ui.Icon(c, workMark).Size(15, 15).TextColor(c.Theme().TextMuted)
			ui.Text(c, "Work runs").Grow(1).FontSize(13)
			ui.Text(c, status).FontSize(11).TextColor(c.Theme().TextMuted)
			ui.Icon(c, arrowMark).Size(13, 13).TextColor(c.Theme().TextMuted)
		})
		for _, item := range active[:min(3, len(active))] {
			ui.ButtonBase(c.Key("open-work-" + item.ID)).Label("Open " + item.Title).FillWidth().
				Gap(8).MinHeight(38).Radius(8).Padding(4).Justify(ui.Start).OnClick(func() {
				a.openWorkInChat(item, a.currentConversation())
			}).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, item.Title).SingleLine().FontSize(12)
					ui.Text(c, workStatus(item.Status)).FontSize(11).TextColor(c.Theme().TextMuted)
				})
				ui.Icon(c, arrowMark).Size(12, 12).TextColor(c.Theme().TextMuted)
			})
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
	ui.Column(c).Gap(8).Padding(8, 10).Radius(13).Background(c.Theme().Surface).Children(func() {
		ui.Row(c).Gap(6).Height(25).Children(func() {
			inspectorHeading(c, "Routines", clockMark)
			smallIcon(c.Key("new-routine"), "New routine", plusMark, a.online && a.chat.ID != "" && !a.chat.Archived && !a.chat.Agent.Removed, a.startRoutine)
		})
		for _, routine := range a.chat.Routines {
			removed := a.budWasRemoved(routine.AgentID)
			requested, pending := a.routinePending[routine.ID]
			enabled := routine.Enabled
			if requested != nil {
				enabled = *requested
			}
			ui.Row(c.Key("routine-" + routine.ID)).Gap(7).MinHeight(34).Children(func() {
				ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, routine.Name).SingleLine().FontSize(12).FontWeight(580).Tooltip(routine.Prompt)
					status := "Paused"
					switch {
					case removed:
						status = "Bud removed"
					case routine.DeviceDisconnected:
						status = "Disconnected"
					case pending:
						status = "Updating…"
					case enabled:
						status = "Next " + routineDate(routine.NextAt, c.Now())
					}
					if routine.Device != "" {
						status = routine.Device + " · " + status
					}
					ui.Text(c, tasks.RoutineCadence(routine.Minutes)+" · "+status).
						FontSize(11).TextColor(c.Theme().TextMuted)
					if routine.ResearchScopeMissing && routine.DeviceID == "" {
						ui.Text(c, "To use web search, recreate this routine.").
							FontSize(11).TextColor(c.Theme().TextMuted)
					}
				})
				canManage := a.online && !a.chat.Archived && !pending
				label := "Resume " + routine.Name
				if enabled {
					label = "Pause " + routine.Name
				}
				ui.Themed(c, routineTheme(c.Theme())).Background(ui.Transparent).Shrink(0).Children(func() {
					toggle := ui.Switch(c.Key("routine-switch-"+routine.ID), &enabled).
						Label(label).Tooltip(label).Disabled(!canManage || removed || routine.DeviceDisconnected)
					if toggle.Changed() && canManage && !removed && !routine.DeviceDisconnected {
						a.changeRoutine(routine.ID, &enabled)
					}
				})
				button := ui.ButtonBase(c.Key("routine-menu-"+routine.ID)).Label("Manage "+routine.Name).Size(20, 26).
					Radius(6).Center().Shrink(0).Disabled(!canManage)
				if button.Hovered() {
					button.Background(c.Theme().SurfacePressed)
				}
				button.Children(func() { ui.Icon(c, moreMark).Size(14, 14).TextColor(c.Theme().TextMuted) })
				button.PopoverMenuButton(func(menu *ui.Menu) {
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
