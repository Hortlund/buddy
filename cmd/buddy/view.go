package main

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"buddy/internal/activitycopy"
	"buddy/internal/tasks"
	"buddy/internal/voiceaudio"

	"github.com/egoist/mygo/ui"
)

var (
	buddyMark = svg(`<path d="M6 5h12a3 3 0 0 1 3 3v7a3 3 0 0 1-3 3h-6l-4.5 3v-3H6a3 3 0 0 1-3-3V8a3 3 0 0 1 3-3Z"/><path d="M8.5 11h.01m7 0h.01" stroke-width="3.2" stroke-linecap="round"/>`)
	agentMark = svg(`<circle cx="12" cy="7.5" r="3.5"/><path d="M5 20v-1.5a5 5 0 0 1 5-5h4a5 5 0 0 1 5 5V20"/>`)
	linkMark  = svg(`<path d="m10 13 4-4m-6 7-1 1a3.5 3.5 0 0 1-5-5l4-4a3.5 3.5 0 0 1 5 0m2 8a3.5 3.5 0 0 0 5 0l4-4a3.5 3.5 0 0 0-5-5l-1 1"/>`)
	repoMark  = svg(`<path d="M3 7a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/>`)
	sendMark  = svg(`<path d="M12 19V5m-5 5 5-5 5 5" stroke-width="2"/>`)
	stopMark  = svg(`<rect x="7" y="7" width="10" height="10" rx="1.8" fill="currentColor" stroke="none"/>`)
)

func svg(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

func (a *desktop) view(c *ui.Context) {
	c.SetTheme(themeFor(a.theme))
	if a.historyNotice.Title != "" {
		c.AddToast(a.historyNotice)
		a.historyNotice = ui.Toast{}
	}
	a.syncWorkspaceFiles()
	a.workspaceSaveNotice(c)
	if a.section != "" || a.showingWorkspaceFiles() {
		a.dictationPending = time.Time{}
	}
	a.refresh(c)
	a.observeChat(c, a.window != nil && a.window.IsFocused())
	a.sendChatReadReceipts(c)
	if a.onboarding {
		a.setupView(c)
		return
	}
	bar := c.TitleBar()
	if a.voice != nil && a.voice.expanded {
		a.callView(c)
		return
	}
	header := max(bar.Height, 44)
	hasInspector := a.section == "" && !a.inspectorClosed
	rightInset := bar.Right
	if hasInspector {
		rightInset = 0
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		a.sidebar(c, header, bar.Left)
		ui.Column(c).Grow(1).MinWidth(0).Background(c.Theme().Background).Children(func() {
			if a.section != "" {
				a.pageNavigation(c, header)
			}
			a.media.view(c)
			switch a.section {
			case "history":
				a.historyPage(c)
			case "menu":
				a.menuPage(c)
			case "buds":
				a.budSettings(c)
			case "agent":
				a.settings(c)
			case "connections":
				a.connections(c)
			case "preferences":
				a.preferences(c)
			case "work":
				a.workPage(c)
			case "autonomy":
				a.autonomyPage(c)
			case "skills":
				a.skillsPage(c)
			case "group":
				a.groupSettings(c)
			case "routine":
				a.routineSettings(c)
			default:
				a.conversation(c, header, rightInset)
			}
			if a.section != "" && a.voice != nil {
				a.voicePanel(c)
			}
		})
		if hasInspector {
			a.inspector(c, header, bar.Right)
		}
	})
}

func (a *desktop) sidebar(c *ui.Context, header, inset float32) {
	width := max(174, inset+84)
	if a.compactSidebar {
		width = max(62, inset+10)
	}
	ui.Column(c).Width(width).Shrink(0).Background(c.Theme().Background.Mix(c.Theme().Surface, .24)).
		BorderWidth(0, 1, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Height(header).Shrink(0).FillWidth().Padding(0, 10, 0, max(14, inset+8)).
			AlignItems(ui.Center).DragWindow().Children(func() {
			if !a.compactSidebar {
				buildWordmark(c.Key("buddy-wordmark"))
			}
		})
		ui.Scroll(c.Key("chat-sidebar")).Grow(1).MinHeight(0).FillWidth().Padding(6, 9, 0).Gap(12).Children(func() {
			ui.Column(c).Gap(3).Children(func() {
				if !a.compactSidebar {
					sidebarHeading(c, "Buds", "New Bud", a.online && !a.saving, a.startAgent)
				}
				if len(a.directory.Agents) == 0 {
					a.budNav(c, tasks.AgentRef{ID: tasks.DefaultAgentID, ConversationID: tasks.DefaultConversationID, Name: "Buddy"})
				}
				for _, bud := range a.directory.Agents {
					a.budNav(c, bud)
				}
			})
			ui.Column(c).Gap(3).Children(func() {
				if !a.compactSidebar && (len(a.directory.Groups) != 0 || len(a.directory.Agents) >= 2) {
					sidebarHeading(c, "Groups", "New group chat", a.online && len(a.directory.Agents) >= 2, a.startGroup)
				}
				for _, chat := range a.directory.Groups {
					a.chatNav(c, chat.ID, chat.Name, groupMark, a.chatHasUnread(chat.ID, chat.HasNewResult, chat.UnreadThrough))
				}
			})
		})
		ui.Column(c).Padding(10, 9).Gap(4).Children(func() {
			if a.compactSidebar {
				ui.Row(c).AlignSelf(ui.Center).Children(func() {
					smallIcon(c.Key("compact-add-bud"), "New Bud", plusMark, a.online && !a.saving, a.startAgent)
					smallIcon(c.Key("compact-add-group"), "New group chat", groupMark, a.online && len(a.directory.Agents) >= 2, a.startGroup)
				})
			}
			a.menuButton(c)
		})
	})
}

func (a *desktop) conversation(c *ui.Context, header, inset float32) {
	padding := max(14, inset+8)
	ui.Row(c).Height(header).Shrink(0).FillWidth().Padding(0, padding, 0, 14).
		Gap(8).DragWindow().Children(func() {
		label := "Compact sidebar"
		if a.compactSidebar {
			label = "Expand sidebar"
		}
		smallIcon(c.Key("toggle-sidebar"), label, sidebarMark, true, func() { a.compactSidebar = !a.compactSidebar })
		ui.Box(c).Grow(1)
		title := a.agentName()
		subtitle := strings.TrimSpace(a.chat.Agent.Role)
		if a.chat.Name != "" {
			title = a.chat.Name
			subtitle = "Lead: " + a.agentName()
			if role := strings.TrimSpace(a.chat.Agent.Role); role != "" {
				subtitle += " · " + role
			}
		}
		btn := ui.ButtonBase(c.Key("toggle-details")).Label(title+", conversation details").
			Description(subtitle).Selected(!a.inspectorClosed).MinWidth(0).MaxWidth(260).Height(34).Radius(17).Padding(0, 11).
			Gap(7).Background(c.Theme().Surface).OnClick(a.toggleInspector)
		if subtitle != "" {
			btn.Height(38).Radius(19)
		}
		if btn.Hovered() {
			btn.Background(c.Theme().SurfacePressed)
		}
		btn.Children(func() {
			switch {
			case a.chat.Name != "":
				ui.Icon(c, groupMark).Size(17, 17).Shrink(0).TextColor(c.Theme().TextMuted)
			default:
				a.drawProfile(c, a.focusedAgent(), 24, false, orbIdle)
			}
			ui.Column(c).MinWidth(0).Gap(0).Children(func() {
				ui.Text(c, title).FontSize(14).FontWeight(600).MinWidth(0).SingleLine().TextColor(c.Theme().Text).Role(ui.RoleHeading)
				if subtitle != "" {
					ui.Text(c, subtitle).FontSize(10).MinWidth(0).SingleLine().TextColor(c.Theme().TextMuted)
				}
			})
		})
		ui.Box(c).Grow(1)
		if !a.chat.Archived && a.chat.Name != "" && len(a.chat.Members) > 1 {
			a.groupLeadPicker(c.Key("group-lead"), "Lead Bud", a.chat.Agent.ID, a.chat.Members,
				a.canSetLeadership() && !a.groupLeadSaving, a.setGroupLead)
		}
		a.conversationHistoryMenu(c)
	})
	a.workspaceTabs(c)
	if a.showingWorkspaceFiles() {
		if files := a.currentWorkspaceFiles(); files != nil {
			files.view(c.Key("files-" + a.currentConversation()))
		} else {
			ui.Text(c, "Workspace files aren’t available right now.").Grow(1).Padding(16).TextColor(c.Theme().TextMuted)
		}
		a.voicePanel(c)
		return
	}
	ui.Column(c).MaxWidth(860).FillWidth().Grow(1).MinHeight(0).AlignSelf(ui.Center).Children(func() {
		a.transcript(c)
		a.codeOutputDialog(c.Key("code-output-dialog"))
		a.voicePanel(c)
		if a.chat.Archived || a.chat.Agent.Removed {
			label := "This Bud was removed."
			if a.chat.Name != "" {
				label = "This group has no active Buds."
			}
			if a.chat.Archived {
				label = "This group is archived."
			}
			ui.Text(c, label).Padding(0, 24, 13).FontSize(12).TextColor(c.Theme().TextMuted)
		} else {
			a.composer(c)
		}
	})
}

func (a *desktop) transcript(c *ui.Context) {
	var work []tasks.WorkItem
	for _, item := range a.chat.Work {
		if (item.Status.Active() || item.Status == tasks.NeedsInput) && (item.CodeRun != nil || item.ProjectID != "") {
			work = append(work, item)
		}
	}
	if len(a.chat.Messages) == 0 && a.chat.Active == nil && len(work) == 0 {
		if a.chat.Agent.Removed {
			ui.Box(c).Grow(1)
		} else {
			a.emptyConversation(c)
		}
	} else {
		first, last := a.list.Visible()
		if first >= 0 && first <= 3 && last >= first && !a.list.AtEnd() && a.historyErr == "" {
			a.loadEarlier()
		}
		if a.loadingHistory || a.historyErr != "" {
			ui.Row(c.Key("history-status")).Padding(6, 30, 0).Gap(8).AlignItems(ui.Center).Children(func() {
				if a.loadingHistory {
					ui.Spinner(c).Size(12, 12)
					ui.Text(c, "Loading earlier messages…").Role(ui.RoleStatus).FontSize(11).TextColor(c.Theme().TextMuted)
				} else {
					ui.Text(c, a.historyErr).FontSize(11).TextColor(c.Theme().TextMuted)
					ui.Button(c.Key("retry-history"), "Retry").Height(24).Disabled(!a.online).OnClick(a.loadEarlier)
				}
			})
		}
		count, active := len(a.chat.Messages), a.chat.Active
		rows := count + len(work)
		activeRow := rows
		if active != nil {
			rows++
		}
		a.list.FollowEnd = true
		a.list.Key = func(i int) any {
			if i == activeRow && active != nil {
				return "reply-" + active.ID
			}
			if i >= count {
				return "work-progress-" + work[i-count].ID
			}
			message := a.chat.Messages[i]
			if message.Role == "assistant" {
				return "reply-" + message.TaskID
			}
			return message.ID
		}
		ui.List(c.Key("transcript"), &a.list, rows, func(i int) {
			if i == activeRow && active != nil {
				a.reply(c, tasks.Message{}, active)
			} else if i >= count {
				a.chatWorkProgress(c, work[i-count])
			} else {
				a.message(c, a.chat.Messages[i])
			}
		}).Grow(1).FillWidth().Padding(16, 24).Gap(14)
	}
}

func (a *desktop) chatWorkProgress(c *ui.Context, item tasks.WorkItem) {
	ui.Column(c.Key(item.ID)).Gap(5).FillWidth().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
			jobIndicator(c, item.Status)
			ui.Text(c, item.Title).FontSize(13).FontWeight(650).SingleLine()
		})
		detail := workStatus(item.Status) + " · " + workerLocation(item)
		if item.ProjectName != "" {
			detail += " · " + item.ProjectName
		}
		ui.Text(c, detail).FontSize(11).TextColor(c.Theme().TextMuted)
		if item.Update != "" {
			ui.Text(c, item.Update).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(2).Selectable()
		}
		if item.CodeRun != nil {
			a.codeRunCard(c.Key("job-command"), item.ID, item.CodeRun, item.Status == tasks.Cancelling)
		}
	})
}

func (a *desktop) reply(c *ui.Context, message tasks.Message, active *tasks.ActiveRun) {
	ui.Row(c).AlignItems(ui.Start).Gap(10).FillWidth().Children(func() {
		id, name := message.AgentID, message.AgentName
		if active != nil {
			id, name = active.AgentID, active.AgentName
		}
		profile := a.replyProfile(id)
		if name == "" {
			name = profile.Name
		}
		if active != nil {
			ui.Column(c.Key("active-reply")).Gap(5).FillWidth().Children(func() {
				a.workingReply(c, active, profile)
				if run := active.CodeRun; run != nil {
					a.codeRunCard(c.Key("active-code-run"), active.ID, run, active.Status == tasks.Cancelling)
				}
			})
			return
		}
		bubble := ui.Column(c.Key("reply-bubble")).MinWidth(0).MaxWidth(610).MinHeight(36).
			Justify(ui.Center).Padding(8, 12).Background(replySurface(c)).Radius(15)
		bubble.ContextMenu(func(menu *ui.Menu) {
			if a.canReact(message) {
				a.reactionMenu(message, menu)
				menu.Separator()
			}
			a.media.menu(menu, a.window, a.ctx, exportDocument{title: name, text: message.Text, dark: c.Theme().Dark})
		})
		bubble.Gap(5).Children(func() {
			if message.Origin == "routine" {
				author := ""
				if a.chat.Name != "" {
					author = name
				}
				ui.Text(c, notificationLabel(author)).FontSize(11).TextColor(c.Theme().TextMuted)
				ui.Text(c, message.Title).FontSize(13).FontWeight(650)
			} else if message.Origin == "voice" || a.chat.Name != "" || (id != "" && id != a.chat.Agent.ID) {
				ui.Text(c, name).FontSize(11).FontWeight(650).TextColor(c.Theme().TextMuted)
			}
			for i := range message.CodeRuns {
				run := &message.CodeRuns[i]
				a.codeRunCard(c.Key("finished-"+run.RunID), message.TaskID, run, false)
			}
			if run := message.CodeRun; run != nil && len(message.CodeRuns) == 0 {
				a.codeRunCard(c.Key("finished-code-run"), message.TaskID, run, false)
			}
			if message.Text != "" {
				a.referenceMarkdown(c.Key("reply-text"), message.Text, message.AgentID, fileReferenceContext{task: message.TaskID})
			}
			if message.GeneratedImage {
				a.generatedImageView(c.Key(message.TaskID), message.TaskID)
			}
			if message.Card != nil {
				a.choiceCard(c, message)
			}
			if message.BudProposal != nil {
				a.budProposalCard(c, message)
			}
			backgroundQuestion := message.BackgroundQuestion
			if a.chat.OpenBackgroundQuestions != nil {
				backgroundQuestion = slices.Contains(a.chat.OpenBackgroundQuestions, message.TaskID)
			}
			if backgroundQuestion {
				ui.Button(c.Key("reply-background"), "Reply").Height(27).AlignSelf(ui.Start).
					Disabled(!a.online || a.sending || a.saving || a.chat.Archived).OnClick(func() {
					id := a.currentConversation()
					if a.backgroundAnswers == nil {
						a.backgroundAnswers = make(map[string]string)
					}
					a.backgroundAnswers[id] = message.TaskID
					delete(a.jobDrafts, id)
					delete(a.selectedSkills, id)
					a.chatErr, a.focusComposer = "", true
				})
			}
			if message.GitHub != nil {
				githubCard(c.Key("github-result"), message.GitHub)
			}
			if message.ConnectionCard != nil {
				a.connectionCard(c.Key("connection-result"), message.ConnectionCard, message.TaskID)
			}
			if message.WatchCard != nil {
				a.connectionCard(c.Key("watch-result"), message.WatchCard, message.TaskID)
			}
			a.messageReactions(c.Key("reply-reactions"), message, ui.Start)
			if message.Origin == "work_input" && message.Status == tasks.NeedsInput {
				ui.Button(c.Key("reply-to-job"), "Reply to job").Height(27).AlignSelf(ui.Start).
					Disabled(!a.online || a.sending || a.saving).OnClick(func() {
					a.askAboutWork(tasks.WorkItem{ID: message.JobID, Title: message.JobTitle,
						ConversationID: message.JobConversationID, InputID: message.TaskID})
				})
			} else if (message.Origin == "handoff" || message.Origin == tasks.WatchOrigin) && message.Title != "" {
				ui.ButtonBase(c.Key("view-job")).Label("View job").Tooltip(message.Title).
					Height(24).Padding(0, 5).Radius(5).AlignSelf(ui.Start).Gap(5).
					TextColor(c.Theme().TextMuted).OnClick(func() {
					a.openWork(tasks.WorkItem{ID: message.TaskID, Title: message.Title, ConversationID: a.currentConversation()})
				}).Children(func() {
					ui.Icon(c, workMark).Size(12, 12)
					ui.Text(c, "View job").FontSize(11)
				})
			}
		})
		messageTime(c, message.At)
	})
}

func activityText(active *tasks.ActiveRun) string {
	if active.Status != tasks.Running {
		return ""
	}
	if text := strings.TrimSpace(active.Update); text != "" {
		return text
	}
	switch active.Kind {
	case tasks.Reading, tasks.Searching, tasks.Editing:
		return active.Activity
	case tasks.RunningTool:
		if _, detail, ok := strings.Cut(active.Activity, " · "); ok && detail != "" {
			return active.Activity
		}
	}
	return ""
}

func replyStatus(active *tasks.ActiveRun, elapsed time.Duration) string {
	switch active.Status {
	case tasks.Cancelling:
		return "Stopping…"
	case tasks.Queued:
		return "Queued"
	}

	if active.Kind == tasks.Stopping {
		return "Stopping…"
	}
	return activitycopy.Label(active.ID, string(active.Kind), int(elapsed/(activitycopy.IntervalSeconds*time.Second)))
}

func (a *desktop) workingReply(c *ui.Context, active *tasks.ActiveRun, profile tasks.Agent) {
	row := ui.Row(c).FillWidth().MinHeight(36).AlignItems(ui.Center).Gap(8).Padding(1, 4)
	type activityClock struct {
		id      string
		kind    tasks.ActivityKind
		status  tasks.Status
		started time.Time
	}
	now := c.Now()
	stage := activityClock{active.ID, active.Kind, active.Status, now}
	clock := ui.Local(row, "activity-clock", func() activityClock { return stage })
	if clock.id != stage.id || clock.kind != stage.kind || clock.status != stage.status {
		*clock = stage
	}
	row.Children(func() {
		label := activityText(active)
		if active.Status == tasks.Running {
			mood := orbMood(active.Kind)
			if mood == orbIdle || mood == orbMood(tasks.Listening) {
				mood = orbMood(tasks.Reasoning)
			}
			a.drawProfile(c, profile, 37, true, mood)
		} else {
			activityPulse(c)
		}
		if active.CodeRun != nil && active.CodeRun.Status == tasks.Running {
			return
		}
		if label == "" {
			elapsed := now.Sub(clock.started)
			label = replyStatus(active, elapsed)
			if active.Status == tasks.Running && active.Kind != tasks.Stopping {
				interval := activitycopy.IntervalSeconds * time.Second
				c.After(interval - elapsed%interval)
			}
		}
		ui.Text(c, label).Role(ui.RoleStatus).MinWidth(0).FontSize(13).TextColor(c.Theme().TextMuted).Selectable()
	})
}

func activityPulse(c *ui.Context) {
	color := c.Theme().Accent
	phase := float64(c.Now().UnixMilli()%1350) / 1350 * 2 * math.Pi
	reduced := c.Preferences().ReduceMotion
	if !reduced {
		c.After(50 * time.Millisecond)
	}
	ui.Box(c).Size(25, 10).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		for i := range 3 {
			strength := float32(.6)
			if !reduced {
				strength = float32((1 + math.Sin(phase-float64(i)*1.3)) / 2)
			}
			d := 3.5 + 2.5*strength
			p.Fill(ui.Rect{X: r.X + 4 + float32(i)*8 - d/2, Y: r.Y + (r.H-d)/2, W: d, H: d}, color.Alpha(.34+.66*strength), d/2)
		}
	})
}

func (a *desktop) emptyConversation(c *ui.Context) {
	ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Gap(12).Children(func() {
		a.drawProfile(c, a.focusedAgent(), 116, true, orbIdle)
		title := a.agentName()
		if a.chat.Name != "" {
			title = a.chat.Name
		}
		ui.Text(c, title).FontSize(26).FontWeight(650).LetterSpacing(-.6).TextAlign(ui.Center)
	})
}

func (a *desktop) message(c *ui.Context, message tasks.Message) {
	if message.Role == "user" {
		ui.Column(c).Gap(6).FillWidth().Children(func() {
			ui.Row(c).FillWidth().Justify(ui.End).AlignItems(ui.Start).Gap(9).Children(func() {
				messageTime(c, message.At)
				ui.Box(c).MaxWidth(530).AlignSelf(ui.End).Background(c.Theme().SurfacePressed).Radius(15).Padding(8, 12).
					MinWidth(0).Children(func() {
					ui.Column(c).Gap(5).Children(func() {
						if message.Origin == "voice" {
							ui.Text(c, "You · Voice").FontSize(11).TextColor(c.Theme().TextMuted)
						} else if message.AnswerTo != "" {
							ui.Text(c, "Answer to: "+message.AnswerTo).FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(2)
						} else if message.SkillName != "" {
							ui.Text(c, "Skill · /"+message.SkillName).FontSize(11).TextColor(c.Theme().TextMuted)
						}
						if message.JobID != "" {
							ui.Button(c.Key("about-job"), "About "+message.JobTitle).MaxWidth(300).AlignSelf(ui.Start).
								OnClick(func() {
									a.openWork(tasks.WorkItem{ID: message.JobID, Title: message.JobTitle, ConversationID: message.JobConversationID})
								})
						}
						if message.Text != "" {
							a.referenceMarkdown(c.Key("sent-text"), message.Text, a.chat.Agent.ID, fileReferenceContext{task: message.TaskID})
						}
						for i, file := range message.Attachments {
							ui.Button(c.Key(i), file.Name).Disabled(!a.online || a.savingFile).
								Tooltip("Save attachment · " + file.Name).OnClick(func() { a.saveAttachment(message.TaskID, i, file.Name) })
						}
					})
				})
			})
			a.messageReactions(c.Key("sent-reactions"), message, ui.End)
			if message.Status == tasks.Queued {
				ui.Row(c).Gap(8).AlignSelf(ui.End).Children(func() {
					label := "Queued for later"
					if message.Activity == tasks.WorkspaceReviewMessage {
						label = message.Activity
					}
					ui.Text(c, label).TextColor(c.Theme().TextMuted).FontSize(11)
					ui.Button(c.Key("remove-"+message.TaskID), "Remove").
						Disabled(!a.online || a.stopping != "").OnClick(func() { a.cancelTurn(message.TaskID, true) })
				})
			}
			for _, correction := range message.Steering {
				ui.Column(c).MaxWidth(620).AlignSelf(ui.End).Gap(4).Padding(8, 12).
					Background(replySurface(c)).Radius(12).Children(func() {
					ui.Text(c, correctionLabel(correction.State)).FontSize(11).TextColor(c.Theme().TextMuted)
					ui.Text(c, correction.Text).FontSize(13).Selectable()
				})
			}

		})
		return
	}
	a.reply(c, message, nil)
}

func messageTime(c *ui.Context, at time.Time) {
	if !at.IsZero() {
		ui.Text(c.Key("message-time"), at.Local().Format("15:04")).
			FontSize(10).TextColor(c.Theme().TextMuted).SingleLine().Shrink(0).Padding(11, 0, 0)
	}
}

func correctionLabel(state string) string {
	switch state {
	case "pending":
		return "Sending course correction…"
	case "missed":
		return "Delivery unconfirmed"
	default:
		return "Course correction"
	}
}

func (a *desktop) composer(c *ui.Context) {
	active := a.chat.Active
	chatID := a.currentConversation()
	job := a.jobDrafts[chatID]
	question := a.backgroundAnswers[chatID]
	if !a.dictationPending.IsZero() {
		if time.Now().After(a.dictationPending) {
			a.dictationPending = time.Time{}
		} else if a.composerInput.Focused(c) {
			a.dictationPending = time.Time{}
			a.window.Update(func() {
				if a.section == "" && !startSystemDictation() {
					a.chatErr = "Use your Mac’s Dictation shortcut, or enable Dictation in Keyboard settings."
				}
			})
		} else {
			c.After(20 * time.Millisecond)
		}
	}
	queued := 0
	for _, message := range a.chat.Messages {
		if message.Status == tasks.Queued {
			queued++
		}
	}
	ui.Column(c).FillWidth().Padding(0, 24, 13).Gap(5).Children(func() {
		if a.serverErr != nil {
			ui.Text(c, "Reconnecting…").Role(ui.RoleStatus).TextColor(c.Theme().Danger).FontSize(12).Tooltip(a.serverErr.Error())
		}
		if a.chatErr != "" {
			ui.Text(c, a.chatErr).TextColor(c.Theme().Danger).FontSize(12)
		}
		if queued > 0 {
			ui.Text(c, fmt.Sprintf("%d queued", queued)).FontSize(11).TextColor(c.Theme().TextMuted)
		}
		if job.ID != "" {
			ui.Row(c.Key("job-reference")).AlignSelf(ui.Start).Gap(5).Padding(2, 4, 2, 8).
				Radius(9).Background(c.Theme().SurfacePressed).Children(func() {
				ui.Icon(c, workMark).Size(13, 13).TextColor(c.Theme().TextMuted)
				label := "About "
				if job.InputID != "" {
					label = "Replying to "
				}
				ui.Text(c, label+job.Title).FontSize(12).MaxLines(1)
				ui.Button(c.Key("remove-job-reference"), "×").Label("Remove job reference").Size(25, 25).
					Disabled(a.sending).OnClick(func() { delete(a.jobDrafts, chatID) })
			})
		}
		if question != "" {
			ui.Row(c.Key("background-reference")).AlignSelf(ui.Start).Gap(5).Padding(2, 4, 2, 8).
				Radius(9).Background(c.Theme().SurfacePressed).Children(func() {
				ui.Text(c, "Replying to background work").FontSize(12)
				ui.Button(c.Key("remove-background-reference"), "×").Label("Clear reply").Size(25, 25).
					Disabled(a.sending).OnClick(func() { delete(a.backgroundAnswers, chatID) })
			})
		}
		a.attachmentDrafts(c)
		a.skillSuggestions(c)
		ui.Row(c.Key("composer-shell")).FillWidth().AlignItems(ui.End).Background(c.Theme().Surface).
			Radius(22).Border(1, c.Theme().Border).Padding(5).Gap(6).Children(func() {
			attachLabel := "Attach files"
			if !a.canSendAttachments() {
				attachLabel = "Update the Buddy server to attach files"
			}
			quietIcon(c.Key("attach"), attachLabel, plusMark, a.sending || a.pickingFiles || !a.canSendAttachments(), a.chooseAttachments)
			placeholder := "Message " + a.agentName() + "…"
			if a.chat.Name != "" {
				placeholder = "Message the group…"
			}
			if job.InputID != "" {
				placeholder = "Reply to this job…"
			}
			if question != "" {
				placeholder = "Reply…"
			}
			input := ui.TextAreaBase(c.Key("chat-input"), &a.draft).Label("Message").
				Placeholder(placeholder).Lines(1, 5).Padding(7, 7).Grow(1).MinWidth(0).MinHeight(34).FontSize(14).
				OnPaste(func(string) bool { return a.pasteAttachment() }).
				OnTextKey(func(k ui.TextKey) bool {
					if a.skillPickerKey(k) {
						return true
					}
					if k.Key != ui.KeyEnter {
						return false
					}
					switch k.Mods {
					case 0, ui.Cmd:
						a.draft = k.Text()
						a.send()
					case ui.Alt | ui.Cmd:
						if active != nil && len(a.attachments) == 0 && job.ID == "" && question == "" {
							a.draft = k.Text()
							a.submit(active.ID)
						}
					default:
						return false
					}
					return true
				}).Bind(&a.composerInput)
			if a.skillCaretPending {
				// Skill names and the trailing space are ASCII.
				end := len(a.draft)
				input.SetTextSelection(end, end)
				a.skillCaretPending = false
			}
			if a.focusComposer {
				a.composerInput.Focus()
				a.focusComposer = false
			}
			if supportsDictation {
				quietIcon(c.Key("dictate"), "Dictate a message", micMark, a.pickingFiles, func() {
					a.composerInput.Focus()
					a.dictationPending = time.Now().Add(time.Second)
				})
			}
			hasDraft := strings.TrimSpace(a.draft) != "" || len(a.attachments) != 0
			canSubmit := a.online && !a.sending && !a.pickingFiles && hasDraft &&
				(len(a.attachments) == 0 || a.canSendAttachments())
			if active == nil || job.InputID != "" || question != "" {
				if !hasDraft && !a.sending && job.ID == "" && question == "" && a.chat.ID != "" && a.voice == nil && voiceaudio.Available() {
					composerButton(c.Key("start-voice"), "Start voice call", "Start voice call", waveformMark, a.online, a.startVoice)
					return
				}
				label := "Send"
				if a.sending {
					label = "Sending…"
				}
				composerButton(c.Key("send"), label, "Send · Enter · ⇧ Enter for a new line", sendMark, canSubmit, a.send)
				return
			}
			if hasDraft {
				ui.Button(c.Key("queue"), "Queue").Height(31).AlignSelf(ui.Center).Disabled(!canSubmit).OnClick(a.send).
					Tooltip("Queue a follow-up · Enter")
				if len(a.attachments) == 0 && job.ID == "" && a.skillForDraft(a.draft) == "" {
					ui.Button(c.Key("steer-"+active.ID), "Steer now").Height(31).AlignSelf(ui.Center).
						Disabled(!canSubmit || !active.CanSteer || active.Status != tasks.Running).
						Tooltip("Send to the current run · ⌥ ⌘ Enter").OnClick(func() { a.submit(active.ID) })
				}
			}
			label := "Stop"
			if active.Status == tasks.Cancelling || a.stopping == active.ID {
				label = "Stopping…"
			}
			composerButton(c.Key("stop-"+active.ID), label, "Stop current work", stopMark,
				a.online && active.Status != tasks.Cancelling && a.stopping == "", func() { a.cancelTurn(active.ID, false) })
		})
	})
}

func composerButton(c *ui.Context, label, help string, mark *ui.SVG, enabled bool, action func()) {
	button := ui.ButtonBase(c).Size(34, 34).Shrink(0).Radius(17).Center().Label(label).
		Tooltip(help).Disabled(!enabled).OnClick(action)
	bg, fg := c.Theme().Accent, c.Theme().AccentText
	if !enabled {
		bg, fg = c.Theme().SurfacePressed, c.Theme().TextMuted
	} else if button.Hovered() {
		bg = c.Theme().AccentHover
	}
	if !c.Preferences().ReduceMotion {
		bg = button.AnimateColor("hover", bg, ui.Animation{Duration: 100 * time.Millisecond})
	}
	button.Background(bg).Children(func() {
		ui.Icon(c, mark).Size(19, 19).TextColor(fg)
	})
}

func (a *desktop) settings(c *ui.Context) {
	back := func() { a.section = a.editReturn }
	title, subtitle := "Edit Bud", "Update this Bud’s profile, instructions, and workspace."
	if a.creatingAgent {
		title, subtitle = "New Bud", "Give your Bud a role and a way of working."
	} else if a.edit.ID == "" {
		subtitle = "Choose how this Bud looks and works with you."
	}
	controlPageLayout(c, title, subtitle, func() {
		if a.edit.ID == "" && !a.creatingAgent {
			label := "Connect to the server to edit this Bud."
			if a.editLoading {
				label = "Loading Bud settings…"
			} else if a.editErr != "" {
				label = a.editErr
			}
			controlPageScroll(c.Key("settings-empty"), func() {
				ui.Text(c, label).TextColor(c.Theme().TextMuted)
				ui.Button(c.Key("agent-back"), "Back").AlignSelf(ui.Start).OnClick(back)
			})
			return
		}
		controlPageScroll(c.Key("settings"), func() {
			a.appearanceSettings(c)
			ui.Column(c).Background(c.Theme().Surface).Radius(13).Border(1, c.Theme().Border).Padding(19).Gap(16).Children(func() {
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Name").FontWeight(650)
					ui.TextInput(c.Key("agent-name"), &a.edit.Name).Label("Bud name").Height(36)
				})
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Role").FontWeight(650)
					ui.TextInput(c.Key("agent-role"), &a.edit.Role).Label("Bud role").
						Placeholder("e.g. Code reviewer").Height(36)
				})
				a.modelSettings(c)
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Instructions").FontWeight(650)
					ui.TextArea(c.Key("agent-instructions"), &a.edit.Instructions).Label("Bud instructions").
						Placeholder("What should your Bud help with?").Height(100)
					if a.edit.ID != "" {
						ui.Text(c, "Server profile · profiles/"+a.edit.ID+"/AGENTS.md").FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
					}
				})
				if a.creatingAgent {
					ui.Text(c, "Buddy creates a dedicated workspace for this Bud’s files.").FontSize(12).TextColor(c.Theme().TextMuted)
				} else {
					ui.Column(c).Gap(5).Children(func() {
						ui.Text(c, "Working folder").FontWeight(650)
						if a.edit.WorkspaceNeedsReview {
							ui.Text(c, "Confirm this folder or choose a new one before Buddy resumes work here.").
								FontSize(12).TextColor(c.Theme().Danger)
						}
						ui.Row(c).Gap(8).Children(func() {
							ui.TextInput(c.Key("agent-workspace"), &a.edit.Workspace).Label("Working folder").
								Placeholder("Use the default workspace").Height(36).Grow(1).MinWidth(0)
							if !a.remoteHost {
								ui.Button(c.Key("browse-workspace"), "Browse…").Height(36).OnClick(a.browse)
							}
						})
						ui.Text(c, "Clear this field to use the Bud’s default workspace.").
							FontSize(12).TextColor(c.Theme().TextMuted)
					})
					ui.Checkbox(c.Key("agent-edits"), &a.edit.AllowEdits, "Allow background work to edit files")
					ui.Text(c, "With edits off, the Bud can inspect files and run read-only commands.").
						FontSize(12).TextColor(c.Theme().TextMuted)
				}
			})
		})
		ui.Row(c).FillWidth().Shrink(0).Gap(8).Padding(12, controlPageInset, 16).
			BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
			ui.Text(c, a.editErr).Grow(1).TextColor(c.Theme().Danger)
			ui.Button(c.Key("agent-cancel"), "Cancel").Height(35).Disabled(a.saving).OnClick(back)
			label := "Save Bud"
			if a.edit.WorkspaceNeedsReview {
				label = "Confirm workspace"
			}
			if a.creatingAgent {
				label = "Create Bud"
			}
			if a.saving {
				label = "Saving…"
			}
			ui.PrimaryButton(c.Key("agent-save"), label).Height(35).
				Disabled(!a.online || a.saving || a.picturePicking || strings.TrimSpace(a.edit.Name) == "").OnClick(a.saveAgent)
		})
	})
}
