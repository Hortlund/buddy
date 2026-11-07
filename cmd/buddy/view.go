package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"buddy/internal/api"
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
	ui.Column(c).Width(186).Shrink(0).Background(c.Theme().SurfaceHover).BorderWidth(0, 1, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Height(header).Shrink(0).FillWidth().Padding(0, 16).
			BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).DragWindow().Children(func() {
			if inset == 0 {
				buildWordmark(c)
			}
		})
		ui.Scroll(c.Key("chat-sidebar")).Grow(1).MinHeight(0).FillWidth().Padding(8, 11, 0).Gap(8).Children(func() {
			if inset != 0 {
				ui.Box(c).Padding(3, 8, 0).Children(func() { buildWordmark(c) })
			}
			ui.Column(c).Gap(2).Children(func() {
				sidebarHeading(c, "Buds", "New Bud", a.online && !a.saving, a.startAgent)
				if len(a.directory.Agents) == 0 {
					a.budNav(c, tasks.AgentRef{ID: tasks.DefaultAgentID, ConversationID: tasks.DefaultConversationID, Name: "Buddy"})
				}
				for _, bud := range a.directory.Agents {
					a.budNav(c, bud)
				}
			})
			ui.Column(c).Gap(2).Children(func() {
				sidebarHeading(c, "Group chats", "New group chat", a.online && len(a.directory.Agents) >= 2, a.startGroup)
				for _, chat := range a.directory.Groups {
					a.chatNav(c, chat.ID, chat.Name, groupMark, a.chatHasUnread(chat.ID, chat.HasNewResult, chat.UnreadThrough))
				}
			})
		})
		ui.Column(c).Padding(8, 11, 0).Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
			a.menuButton(c)
		})
		text, dot := "Connecting…", ui.Hex("#72867C")
		switch {
		case a.online:
			text, dot = "Server connected", ui.Hex("#2A815B")
		case a.loaded:
			text, dot = "Server offline", c.Theme().Danger
		}
		ui.Row(c).Padding(15, 19).Gap(8).Tooltip(a.endpoint).Children(func() {
			ui.Box(c).Size(6, 6).Radius(99).Background(dot)
			ui.Text(c, text).FontSize(11).TextColor(c.Theme().TextMuted)
		})
	})
}

func (a *desktop) conversation(c *ui.Context, header, inset float32) {
	ui.Row(c).Height(header).Shrink(0).FillWidth().Padding(0, max(16, inset+8), 0, 20).
		Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).DragWindow().Children(func() {
		title, subtitle := a.agentName(), budRole(a.chat.Agent.Role)
		if a.chat.Name != "" {
			title = a.chat.Name
			subtitle = "Lead: " + a.chat.Agent.Name + " · " + subtitle
			ui.Box(c).Size(28, 28).Radius(14).Background(c.Theme().SurfacePressed).Center().Children(func() {
				ui.Icon(c, groupMark).Size(17, 17).TextColor(c.Theme().Text)
			})
		} else {
			ui.ButtonBase(c.Key("profile-picture")).Size(28, 28).Label("Customize profile picture").
				Disabled(!a.online || a.chat.Agent.Removed).OnClick(a.showSettings).Children(func() {
				a.drawProfile(c, a.chat.Agent, 28, false, orbIdle)
			})
		}
		if a.chat.Archived || a.chat.Agent.Removed {
			subtitle = "Chat history"
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(14).FontWeight(680).SingleLine().MinWidth(0).Role(ui.RoleHeading)
				if a.chat.Name == "" && a.chat.Agent.ID != "" && a.chat.Agent.ID == a.directory.PrimaryAgentID {
					primaryBadge(c)
				}
			})
			ui.Text(c, subtitle).FontSize(11).SingleLine().TextColor(c.Theme().TextMuted).Tooltip(subtitle)
		})
		if !a.chat.Archived && a.chat.Name != "" && len(a.chat.Members) > 1 {
			a.groupLeadPicker(c.Key("group-lead"), "Lead Bud", a.chat.Agent.ID, a.chat.Members,
				a.canSetLeadership() && !a.groupLeadSaving, a.setGroupLead)
		}
		a.conversationHistoryMenu(c)
		if a.voice == nil && !a.chat.Archived && !a.chat.Agent.Removed {
			smallIcon(c.Key("start-voice"), "Start voice call", callMark, a.online && a.chat.ID != "" && voiceaudio.Available(), a.startVoice)
		}
		label := "Hide workspace"
		if a.inspectorClosed {
			label = "Show workspace"
		}
		smallIcon(c.Key("toggle-details"), label, detailsMark, true, a.toggleInspector)
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
	ui.Column(c).MaxWidth(780).FillWidth().Grow(1).MinHeight(0).AlignSelf(ui.Center).Children(func() {
		a.transcript(c)
		a.voicePanel(c)
		if a.chat.Archived || a.chat.Agent.Removed {
			label := "This Bud was removed."
			if a.chat.Name != "" {
				label = "This group has no active Buds."
			}
			if a.chat.Archived {
				label = "This group is archived. Restore it from the chat menu to continue."
			}
			ui.Text(c, label).Padding(0, 24, 13).FontSize(12).TextColor(c.Theme().TextMuted)
		} else {
			a.composer(c)
		}
	})
}

func (a *desktop) transcript(c *ui.Context) {
	if len(a.chat.Messages) == 0 {
		if a.chat.Agent.Removed {
			ui.Box(c).Grow(1)
		} else {
			a.emptyConversation(c)
		}
	} else {
		if a.chat.Before != 0 {
			ui.Row(c).Padding(7, 30, 0).Gap(10).Children(func() {
				label := "Load earlier messages"
				if a.loadingHistory {
					label = "Loading earlier messages…"
				}
				ui.Button(c.Key("load-earlier"), label).Disabled(a.loadingHistory || !a.online).OnClick(a.loadEarlier)
				if a.historyErr != "" {
					ui.Text(c, a.historyErr).TextColor(c.Theme().Danger)
				}
			})
		}
		count, active := len(a.chat.Messages), a.chat.Active
		rows := count
		if active != nil {
			rows++
		}
		a.list.FollowEnd = true
		a.list.Key = func(i int) any {
			if i == count && active != nil {
				return "reply-" + active.ID
			}
			message := a.chat.Messages[i]
			if message.Role == "assistant" {
				return "reply-" + message.TaskID
			}
			return message.ID
		}
		ui.List(c.Key("transcript"), &a.list, rows, func(i int) {
			if i == count {
				a.reply(c, tasks.Message{}, active)
			} else {
				a.message(c, a.chat.Messages[i])
			}
		}).Grow(1).FillWidth().Padding(16, 24).Gap(14)
	}
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
		mood, animated := orbIdle, active != nil
		if active != nil {
			mood = orbMood(active.Kind)
		} else if age := c.Now().Sub(message.At); age >= 0 && age < 3*time.Second {
			animated = true
			c.After(3*time.Second - age)
			switch {
			case message.Status == tasks.Failed || message.Status == tasks.Interrupted:
				mood = orbUnsure
			case message.Status == tasks.Succeeded && message.DidWork:
				mood = orbProud
			case message.Status == tasks.Succeeded:
				mood = orbHappy
			}
		}
		a.drawProfile(c, profile, 30, animated, mood)
		bubble := ui.Column(c.Key("reply-bubble")).MinWidth(0).MaxWidth(610).MinHeight(36).
			Justify(ui.Center).Padding(8, 12).Background(replySurface(c)).Radius(15)
		if active == nil && !message.At.IsZero() {
			bubble.Tooltip(name + " · " + message.At.Local().Format("15:04"))
		}
		if active == nil && a.canReact(message) {
			bubble.ContextMenu(func(menu *ui.Menu) { a.reactionMenu(message, menu) })
		}
		opacity := float32(1)
		if active != nil {
			opacity = .83
		}
		if !c.Preferences().ReduceMotion {
			opacity = bubble.Animate("reply-opacity", opacity, 160*time.Millisecond)
		}
		bubble.Opacity(opacity).Gap(4).Children(func() {
			if active == nil && message.Origin == "routine" {
				author := ""
				if a.chat.Name != "" {
					author = name
				}
				ui.Text(c, notificationLabel(message, author)).FontSize(11).TextColor(c.Theme().TextMuted)
				ui.Text(c, message.Title).FontSize(13).FontWeight(650)
			} else if message.Origin == "voice" || a.chat.Name != "" {
				ui.Text(c, name).FontSize(11).FontWeight(650).TextColor(c.Theme().TextMuted)
			}
			if active == nil {
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
				if message.GitHub != nil {
					githubCard(c.Key("github-result"), message.GitHub)
				}
				if message.ConnectionCard != nil {
					a.connectionCard(c.Key("connection-result"), message.ConnectionCard, message.TaskID)
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
			} else if text := activityText(active); text != "" {
				ui.Text(c.Key("activity-text"), text).MinWidth(0).FontSize(14).LineHeight(1.45).Selectable()
			} else {
				activityPulse(c)
			}
		})
		if active == nil && message.ID != 0 && len(a.chat.ReactionOptions) != 0 && !a.chat.Archived && !a.chat.Agent.Removed {
			a.reactionButton(c.Key("react"), message)
		}
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

func activityPulse(c *ui.Context) {
	color := c.Theme().Text
	phase := float64(c.Now().UnixMilli()%1350) / 1350 * 2 * math.Pi
	reduced := c.Preferences().ReduceMotion
	if !reduced {
		c.After(60 * time.Millisecond)
	}
	ui.Box(c).Size(17, 5).Shrink(0).Role(ui.RoleStatus).Label("Buddy is working").Draw(func(p *ui.Painter, r ui.Rect) {
		for i := range 3 {
			alpha := float32(.55)
			if !reduced {
				alpha = .24 + .63*float32((1+math.Sin(phase-float64(i)*.85))/2)
			}
			p.Fill(ui.Rect{X: r.X + float32(i)*6, Y: r.Y + 1, W: 3, H: 3}, color.Alpha(alpha), 1.5)
		}
	})
}

func (a *desktop) emptyConversation(c *ui.Context) {
	ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Gap(13).Children(func() {
		a.drawProfile(c, a.focusedAgent(), 108, true, orbIdle)
		ui.Text(c, "What can I take off your plate?").FontSize(32).Bold().LetterSpacing(-.9).TextAlign(ui.Center)
		ui.Text(c, "Talk through an idea or give me something to take care of.").FontSize(14).TextColor(c.Theme().TextMuted).TextAlign(ui.Center)
		ui.Row(c).Gap(8).Wrap().Justify(ui.Center).Margin(5, 0, 0).Children(func() {
			ui.Button(c.Key("starter-plan"), "Plan my workday").OnClick(func() {
				a.draft = "Help me plan my workday."
			})
			ui.Button(c.Key("starter-repo"), "Look into a project").OnClick(func() {
				a.draft = "Take a look at my working folder and give me a useful overview."
			})
		})
	})
}

func (a *desktop) message(c *ui.Context, message tasks.Message) {
	if message.Role == "user" {
		ui.Column(c).Gap(6).FillWidth().Children(func() {
			ui.Box(c).MaxWidth(530).AlignSelf(ui.End).Background(c.Theme().SurfacePressed).Radius(15).Padding(8, 12).
				Tooltip("You · " + message.At.Local().Format("15:04")).Children(func() {
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
			ui.Text(c, "Can't reach the Buddy server. Reconnecting…").TextColor(c.Theme().Danger).FontSize(12).Tooltip(a.serverErr.Error())
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
		a.attachmentDrafts(c)
		if job.InputID == "" {
			a.skillSuggestions(c)
		}
		ui.Row(c.Key("composer-shell")).FillWidth().AlignItems(ui.End).Background(c.Theme().Surface).
			Radius(16).Border(1, c.Theme().Border).Padding(5).Gap(6).Children(func() {
			attachLabel := "Attach files"
			if !a.canSendAttachments() {
				attachLabel = "Update the Buddy server to attach files"
			}
			quietIcon(c.Key("attach"), attachLabel, attachMark, a.sending || a.pickingFiles || !a.canSendAttachments(), a.chooseAttachments)
			placeholder := "Message " + a.agentName() + "…"
			if a.chat.Name != "" {
				placeholder = "Message the group…"
			}
			if job.InputID != "" {
				placeholder = "Reply to this job…"
			}
			input := ui.TextAreaBase(c.Key("chat-input"), &a.draft).Label("Message").
				Placeholder(placeholder).Lines(1, 5).Padding(7, 7).Grow(1).MinWidth(0).MinHeight(34).FontSize(14).
				OnPaste(func(string) bool { return a.pasteAttachment() }).
				OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyTab && k.Mods == 0 && a.online && a.serverProtocol >= api.SkillSelectionProtocol && job.InputID == "" && !a.sending {
						if matches := skillMatches(k.Text(), a.directory.Skills); len(matches) > 0 {
							a.fillSkill(matches[0].Name)
							k.SetText(a.draft)
							return true
						}
					}
					if k.Key != ui.KeyEnter {
						return false
					}
					switch k.Mods {
					case 0, ui.Cmd:
						a.draft = k.Text()
						a.send()
					case ui.Alt | ui.Cmd:
						if active != nil && len(a.attachments) == 0 && job.ID == "" {
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
			if active == nil || job.InputID != "" {
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

func pageHeading(c *ui.Context, title, subtitle string) {
	ui.Column(c).Gap(7).Children(func() {
		ui.Text(c, title).FontSize(34).FontWeight(700).LetterSpacing(-1).Role(ui.RoleHeading)
		ui.Text(c, subtitle).FontSize(14).TextColor(c.Theme().TextMuted)
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

func (a *desktop) connections(c *ui.Context) {
	controlPage(c.Key("connections"), "Connections", "Choose where Buddy lives and what it can use.", nil, func() {
		a.hosting(c)
		a.mobileDevices(c)
		a.runtimes(c)
		a.executionSettings(c)
		a.mcpSettings(c)
		a.integrationSettings(c)
	})
}
