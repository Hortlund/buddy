package main

import (
	"slices"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

var workMark = svg(`<rect x="3" y="7" width="18" height="13" rx="2"/><path d="M8 7V5a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2M3 12a22 22 0 0 0 18 0m-9 1v2"/>`)

type workBrowser struct {
	scope            string
	scopeVersion     uint64
	selectionVersion uint64
	pollVersion      uint64
	items            []tasks.WorkItem
	selected         tasks.WorkItem
	detail           tasks.WorkDetails
	scroll           ui.ScrollState
	filter           string
	err              string
	detailErr        string
	stopErr          string
	loading          bool
	reading          bool
	nextPoll         time.Time
	nextRead         time.Time
	older            []tasks.WorkItem
	olderCursor      string
	olderLoaded      bool
	olderBusy        bool
	olderErr         string
}

func (a *desktop) openWork(item tasks.WorkItem) {
	a.openWorkInChat(item, "")
}

func (a *desktop) openWorkInChat(item tasks.WorkItem, chat string) {
	if a.sending || a.saving {
		return
	}
	a.section = "work"
	a.work.setScope(chat)
	a.work.nextPoll = time.Time{}
	a.work.selectItem(item)
}

func (work *workBrowser) selectItem(item tasks.WorkItem) {
	work.selectionVersion++
	work.selected, work.detail, work.detailErr = item, tasks.WorkDetails{}, ""
	work.stopErr = ""
	work.scroll.X, work.scroll.Y = 0, 0
	work.reading, work.nextRead = false, time.Time{}
}

func (a *desktop) stopWork(work *workBrowser, id string) {
	scope, selection := work.scopeVersion, work.selectionVersion
	if a.stopTask(id, false, func(_ tasks.Task, err error) {
		if err == nil || work.scopeVersion != scope {
			return
		}
		work.nextPoll = time.Time{}
		if work.selectionVersion == selection {
			work.nextRead = time.Time{}
			work.stopErr = errorMessage(err, "Couldn't stop the job. Try again.")
		}
	}) {
		work.stopErr = ""
	}
}

func (work *workBrowser) noteStopped(task tasks.Task) {
	// A read started before the stop can complete after its response.
	work.pollVersion++
	work.loading, work.reading = false, false
	work.nextPoll, work.nextRead = time.Time{}, time.Time{}
	update := func(item *tasks.WorkItem) {
		if item.ID != task.ID || (!item.Status.Active() && item.Status != tasks.NeedsInput) {
			return
		}
		item.Status, item.InputID, item.Update = task.Status, "", ""
		item.Activity, item.FinishedAt = task.Summary, task.FinishedAt
	}
	for i := range work.items {
		update(&work.items[i])
	}
	update(&work.selected)
	update(&work.detail.WorkItem)
	if work.detail.ID == task.ID {
		work.detail.Question = ""
	}
}

func (work *workBrowser) setScope(scope string) {
	if work.scope != scope {
		*work = workBrowser{scope: scope, scopeVersion: work.scopeVersion + 1}
	}
}

func (a *desktop) refreshWork(c *ui.Context, work *workBrowser, scope string) {
	if a.window == nil || !a.online || a.serverProtocol < api.WorkbenchProtocol {
		return
	}
	work.setScope(scope)
	version, poll := work.scopeVersion, work.pollVersion
	client, win := a.client, a.window
	if !work.loading {
		if wait := time.Until(work.nextPoll); wait > 0 {
			c.After(wait)
		} else {
			work.loading = true
			work.nextPoll = time.Now().Add(2 * time.Second)
			go func() {
				items, err := client.WorkOverview(a.ctx, scope)
				win.Update(func() {
					if work.scopeVersion != version || work.pollVersion != poll {
						return
					}
					work.loading = false
					if err != nil {
						work.err = errorMessage(err, "Couldn't load work. Retrying…")
						return
					}
					work.items, work.err = items, ""
				})
			}()
		}
	}
	selected := work.selected
	if selected.ID == "" || work.reading {
		return
	}
	if wait := time.Until(work.nextRead); wait > 0 {
		c.After(wait)
		return
	}
	work.reading = true
	work.nextRead = time.Now().Add(2 * time.Second)
	selection := work.selectionVersion
	go func() {
		detail, err := client.WorkDetails(a.ctx, selected.ConversationID, selected.ID)
		win.Update(func() {
			work.finishDetailRead(version, poll, selection, detail, err)
		})
	}()
}

func (work *workBrowser) finishDetailRead(scope, poll, selection uint64, detail tasks.WorkDetails, err error) {
	if work.scopeVersion != scope || work.pollVersion != poll || work.selectionVersion != selection {
		return
	}
	work.reading = false
	if err != nil {
		work.detailErr = errorMessage(err, "Couldn't load job details. Retrying…")
		return
	}
	work.detail, work.detailErr = detail, ""
}

func (a *desktop) workPage(c *ui.Context) {
	work := &a.work
	a.refreshWork(c, work, work.scope)
	controlPageLayout(c, "Work", "Follow what your Buds are doing and revisit finished jobs.", func() {
		if a.online && a.serverProtocol != 0 && a.serverProtocol < api.WorkbenchProtocol {
			controlPageScroll(c.Key("work-unavailable"), func() {
				ui.Text(c, "Update the Buddy server to open the work overview.").TextColor(c.Theme().TextMuted)
			})
			return
		}
		ui.Row(c).Grow(1).MinHeight(0).FillWidth().Padding(0, controlPageInset, controlPageBottom).
			Gap(13).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(270).Shrink(0).MinHeight(0).Gap(7).Children(func() {
				if work.scope != "" {
					ui.Button(c.Key("all-work"), "Show jobs from all chats").AlignSelf(ui.Start).Height(28).
						OnClick(func() { a.openWork(tasks.WorkItem{}) })
				}
				ui.TextInput(c.Key("work-filter"), &work.filter).Label("Filter jobs").Placeholder("Search jobs…").Height(33)
				ui.Scroll(c.Key("work-list")).Grow(1).MinHeight(0).FillWidth().Children(func() {
					ui.Column(c).FillWidth().Padding(0, 11, 5, 0).Gap(8).Children(func() {
						workList(c, work, work.scope == "")
						a.olderWorkButton(c, work)
					})
				})
			})
			ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).Border(1, c.Theme().Border).
				Radius(12).Background(c.Theme().Surface).Children(func() {
				ui.Scroll(c.Key("work-detail")).TrackScroll(&work.scroll).Grow(1).FillWidth().MinWidth(0).MinHeight(0).Children(func() {
					ui.Column(c).FillWidth().MinWidth(0).Padding(15, 23, 15, 15).Children(func() {
						a.workDetail(c, work, false)
					})
				})
			})
		})
	})
}

func workList(c *ui.Context, work *workBrowser, showChat bool) {
	if work.err != "" {
		ui.Text(c, work.err).TextColor(c.Theme().Danger).FontSize(12)
	}
	filter := strings.ToLower(strings.TrimSpace(work.filter))
	items := work.visibleItems()
	found := false
	for _, group := range []struct {
		name string
		show func(tasks.Status) bool
	}{
		{"Waiting for input", func(s tasks.Status) bool { return s == tasks.NeedsInput }},
		{"In progress", tasks.Status.Active},
		{"Recent", func(s tasks.Status) bool { return !s.Active() && s != tasks.NeedsInput }},
	} {
		shown := false
		for _, item := range items {
			search := strings.ToLower(item.Title + " " + item.AgentName + " " + item.ConversationName + " " + workerLocation(item))
			if !group.show(item.Status) || !strings.Contains(search, filter) {
				continue
			}
			if !shown {
				sectionLabel(c, group.name)
				shown = true
			}
			found = true
			workRow(c.Key("job-"+item.ID), item, work.selected.ID == item.ID, showChat, func() { work.selectItem(item) })
		}
	}
	if !found && work.err == "" {
		label := "No background jobs yet."
		if work.loading {
			label = "Loading work…"
		} else if len(work.items) != 0 {
			label = "No jobs match your search."
		}
		ui.Text(c, label).TextColor(c.Theme().TextMuted).FontSize(12)
	}
}

func (work *workBrowser) visibleItems() []tasks.WorkItem {
	if len(work.older) == 0 {
		return work.items
	}
	items := slices.Clone(work.items)
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		seen[item.ID] = true
	}
	for _, item := range work.older {
		if !seen[item.ID] {
			items = append(items, item)
			seen[item.ID] = true
		}
	}
	return items
}

func (a *desktop) olderWorkButton(c *ui.Context, work *workBrowser) {
	limit := tasks.WorkOverviewLimit
	if work.scope != "" {
		limit = tasks.ChatWorkOverviewLimit
	}
	if a.serverProtocol < api.WorkHistoryProtocol || (work.olderLoaded && work.olderCursor == "") ||
		(!work.olderLoaded && len(work.items) < limit) {
		return
	}
	label := "Load older jobs"
	if work.olderBusy {
		label = "Loading older jobs…"
	}
	ui.Button(c.Key("load-older-work"), label).FillWidth().Disabled(work.olderBusy || !a.online || a.window == nil).
		OnClick(func() { a.loadOlderWork(work) })
	if work.olderErr != "" {
		ui.Text(c, work.olderErr).FontSize(11).TextColor(c.Theme().Danger)
	}
}

func (a *desktop) loadOlderWork(work *workBrowser) {
	before := work.olderCursor
	if !work.olderLoaded {
		for i := len(work.items) - 1; i >= 0; i-- {
			item := work.items[i]
			if !item.Status.Active() && item.Status != tasks.NeedsInput {
				before = item.ID
				break
			}
		}
	}
	chat, win, client := work.scope, a.window, a.client
	version := work.scopeVersion
	work.olderBusy, work.olderErr = true, ""
	go func() {
		page, err := client.WorkHistory(a.ctx, chat, before)
		win.Update(func() {
			if work.scopeVersion != version {
				return
			}
			work.olderBusy = false
			if err != nil {
				work.olderErr = errorMessage(err, "Couldn't load older jobs.")
				return
			}
			work.older = append(work.older, page.Items...)
			work.olderCursor, work.olderLoaded = page.Next, true
		})
	}()
}

func workerLocation(item tasks.WorkItem) string {
	if item.Device != "" {
		return item.Device
	}
	return "Buddy server"
}

func jobColor(c *ui.Context, status tasks.Status) ui.Color {
	switch status {
	case tasks.Running, tasks.Cancelling:
		if !c.Theme().Dark {
			return ui.Hex("#0969DA")
		}
		return c.Theme().Accent
	case tasks.Succeeded:
		return c.Theme().Success
	case tasks.Failed, tasks.Interrupted:
		return c.Theme().Danger
	case tasks.NeedsInput:
		return c.Theme().Warning
	default:
		return c.Theme().TextMuted
	}
}

func jobIndicator(c *ui.Context, status tasks.Status) {
	color := jobColor(c, status)
	if (status == tasks.Running || status == tasks.Cancelling) && !c.Preferences().ReduceMotion {
		ui.Spinner(c).Size(11, 11).TextColor(color).Label(workStatus(status))
		return
	}
	ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(color)
}

func workRow(c *ui.Context, item tasks.WorkItem, selected, showChat bool, open func()) {
	btn := ui.ButtonBase(c).Label("Open "+item.Title).FillWidth().Padding(8, 9).Radius(8).
		Border(1, c.Theme().Border).Justify(ui.Start).Background(c.Theme().Surface).OnClick(open)
	if selected {
		btn.Border(1, c.Theme().Accent).Background(c.Theme().SurfacePressed)
	} else if btn.Hovered() {
		btn.Background(c.Theme().SurfaceHover)
	}
	btn.Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, item.Title).FontSize(13).FontWeight(650).SingleLine().Tooltip(item.Title)
			ui.Row(c).Gap(6).Children(func() {
				jobIndicator(c, item.Status)
				text := workStatus(item.Status) + " · " + workerLocation(item)
				ui.Text(c, text).FontSize(11).TextColor(c.Theme().TextMuted).SingleLine()
			})
			worker := item.AgentName + "’s worker"
			if showChat && item.ConversationName != "" && item.ConversationName != item.AgentName {
				worker += " · " + item.ConversationName
			}
			ui.Text(c, worker).FontSize(11).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(worker)
		})
	})
}

func (a *desktop) workDetail(c *ui.Context, work *workBrowser, compact bool) {
	item := work.selected
	if item.ID == "" {
		ui.Text(c, "Choose a job to see its progress and result.").TextColor(c.Theme().TextMuted).FontSize(13)
		return
	}
	detail := work.detail
	if detail.ID == item.ID {
		item = detail.WorkItem
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(11).Children(func() {
		ui.Text(c, item.Title).Role(ui.RoleHeading).FontSize(19).FontWeight(690)
		ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
			for _, bud := range a.directory.Agents {
				if bud.ID == item.AgentID {
					a.drawProfile(c, a.directoryProfile(bud), 24, false, orbIdle)
					break
				}
			}
			if item.AgentName != "" {
				ui.Text(c, item.AgentName+"’s worker").FontSize(12).FontWeight(620).SingleLine()
			}
		})
		ui.Text(c, workStatus(item.Status)+" · "+workerLocation(item)).FontSize(12).TextColor(jobColor(c, item.Status))
		if !item.CreatedAt.IsZero() {
			timing := "Queued " + item.CreatedAt.Local().Format("Jan 2, 15:04")
			if item.StartedAt != nil {
				timing += " · Started " + item.StartedAt.Local().Format("15:04")
			}
			if item.FinishedAt != nil {
				timing += " · Ended " + item.FinishedAt.Local().Format("15:04")
			}
			ui.Text(c, timing).FontSize(11).TextColor(c.Theme().TextMuted)
		}
		ui.Row(c).Gap(7).Wrap().Children(func() {
			switch item.Status {
			case tasks.Queued, tasks.Running, tasks.NeedsInput:
				label := "Stop job"
				if a.stopping == item.ID {
					label = "Stopping…"
				}
				ui.Button(c.Key("stop-work"), label).TextColor(c.Theme().Danger).
					Disabled(!a.online || a.stopping != "").OnClick(func() { a.stopWork(work, item.ID) })
			}
			archived := a.workChatArchived(item)
			if !archived {
				label := "Ask about this job"
				if item.InputID != "" {
					label = "Reply to job"
				} else if compact {
					label = "Ask in chat"
				}
				ui.Button(c.Key("ask-work"), label).Disabled(a.sending || a.saving || item.ConversationID == "").
					OnClick(func() { a.askAboutWork(item) })
			}
			if !compact || archived {
				ui.Button(c.Key("work-open-chat"), "Open chat").Disabled(item.ConversationID == "").
					OnClick(func() { a.openConversation(item.ConversationID) })
			}
		})
		if work.stopErr != "" {
			ui.Text(c, work.stopErr).FontSize(12).TextColor(c.Theme().Danger)
		}
		if work.detailErr != "" {
			ui.Text(c, work.detailErr).FontSize(12).TextColor(c.Theme().Danger)
		}
		if detail.ID == "" {
			ui.Text(c, "Loading details…").FontSize(12).TextColor(c.Theme().TextMuted)
			return
		}
		if progress := workActivity(item); progress != "" && !slices.ContainsFunc(detail.Events, func(event tasks.Event) bool {
			return event.Text == progress
		}) {
			messageMarkdown(c.Key("job-progress"), progress)
		}
		if detail.Question != "" {
			sectionLabel(c, "Waiting for your reply")
			messageMarkdown(c.Key("job-question"), detail.Question)
		}
		if detail.Result != "" {
			sectionLabel(c, "Result")
			messageMarkdown(c.Key("job-result"), detail.Result)
		}
		if detail.Brief != "" {
			sectionLabel(c, "Assignment")
			messageMarkdown(c.Key("job-brief"), detail.Brief)
		}
		sectionLabel(c, "Activity")
		if len(detail.Events) == 0 {
			ui.Text(c, "No updates yet.").FontSize(12).TextColor(c.Theme().TextMuted)
		}
		for i := len(detail.Events) - 1; i >= 0; i-- {
			event := detail.Events[i]
			ui.Row(c.Key(event.Seq)).Gap(8).AlignItems(ui.Start).Children(func() {
				ui.Text(c, event.At.Local().Format("15:04")).FontSize(11).TextColor(c.Theme().TextMuted).Shrink(0)
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					if event.Stream == "commentary" {
						messageMarkdown(c.Key("update"), event.Text)
					} else {
						ui.Text(c, event.Text).FontSize(12).MinWidth(0).Selectable()
					}
				})
			})
		}
		if len(item.Steering) > 0 {
			sectionLabel(c, "Course corrections")
			for _, correction := range item.Steering {
				ui.Text(c, correctionLabel(correction.State)).FontSize(11).TextColor(c.Theme().TextMuted)
				messageMarkdown(c, correction.Text)
			}
		}
	})
}

func (a *desktop) workChatArchived(item tasks.WorkItem) bool {
	return item.ChatArchived || (item.ConversationID == a.currentConversation() && (a.chat.Agent.Removed || a.chat.Archived)) ||
		slices.ContainsFunc(a.directory.RemovedAgents, func(bud tasks.AgentRef) bool {
			return bud.ConversationID == item.ConversationID
		})
}

func (a *desktop) askAboutWork(item tasks.WorkItem) {
	if a.sending || a.saving || a.workChatArchived(item) {
		return
	}
	if a.voice != nil {
		a.voice.expanded = false
	}
	a.openConversation(item.ConversationID)
	if a.currentConversation() != item.ConversationID {
		return
	}
	if a.jobDrafts == nil {
		a.jobDrafts = make(map[string]tasks.WorkItem)
	}
	a.jobDrafts[item.ConversationID] = item
	if item.InputID == "" && strings.TrimSpace(a.draft) == "" {
		a.draft = "What’s the latest on this job?"
	}
	a.focusComposer = true
}

func (a *desktop) callWorkPanel(c *ui.Context, call *desktopVoice) {
	ui.Themed(c, darkBuddyTheme).Width(306).Shrink(0).MinHeight(0).Children(func() {
		ui.Column(c).Fill().Gap(8).Padding(11).Radius(12).Border(1, callEdge).Background(callTile).Children(func() {
			ui.Row(c).Gap(7).Children(func() {
				if call.work.selected.ID != "" {
					ui.Button(c.Key("back-to-call-work"), "All jobs").OnClick(func() { call.work.selectItem(tasks.WorkItem{}) })
				} else {
					ui.Text(c, "Work in this chat").FontSize(14).FontWeight(650).Grow(1)
				}
			})
			if call.work.selected.ID != "" {
				ui.Scroll(c.Key("call-job-detail")).TrackScroll(&call.work.scroll).Grow(1).MinHeight(0).FillWidth().Children(func() {
					a.workDetail(c, &call.work, true)
				})
			} else {
				ui.Scroll(c.Key("call-jobs")).Grow(1).MinHeight(0).FillWidth().Gap(7).Children(func() {
					hint := "Open a job to see its progress or stop it."
					if a.serverProtocol >= api.VoiceStopWorkProtocol {
						hint = "Say “stop that job” or open a job to see its progress and controls."
					}
					ui.Text(c, hint).FontSize(11).TextColor(c.Theme().TextMuted)
					workList(c, &call.work, false)
				})
			}
		})
	})
}
