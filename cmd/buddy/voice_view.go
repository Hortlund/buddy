package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

var callMark = svg(`<path d="m5.5 4 3-.5 2 4.5-2 1.8a13 13 0 0 0 5.7 5.7l1.8-2 4.5 2-.5 3a2 2 0 0 1-2.2 1.7A16.5 16.5 0 0 1 3.8 6.2 2 2 0 0 1 5.5 4Z"/>`)

func (a *desktop) voiceSettings(c *ui.Context) {
	compatible := a.serverProtocol >= api.VoiceProtocol
	ui.Row(c).Padding(11, 13).Gap(9).AlignItems(ui.Center).Children(func() {
		connectionDetail(c, callMark, "Live voice", "Calls with one or more Buds")
		label := "Set up"
		switch {
		case !a.online:
			label = "Server offline"
		case !compatible && !a.protocolChecked.IsZero():
			label = "Update server"
		case a.voiceAuth.Source != "":
			label = "Configured"
		case !a.voiceAuthLoaded:
			label = "Available"
		}
		connectionBadge(c, label, a.voiceAuth.Source != "")
		action := "Set up"
		if a.voiceAuth.Source != "" {
			action = "Manage"
		}
		if a.voiceKeyOpen {
			action = "Done"
		}
		ui.Button(c.Key("manage-voice"), action).Height(30).Disabled(!a.online || !compatible).OnClick(func() {
			a.voiceKeyOpen = !a.voiceKeyOpen
			if a.voiceKeyOpen {
				a.changeVoiceKey(http.MethodGet)
			} else {
				a.voiceKeyDraft = ""
			}
		})
	})
	if !a.voiceKeyOpen || !compatible {
		return
	}
	ui.Column(c).Padding(9, 13, 12).Gap(7).Children(func() {
		detail := "Use an OpenAI API key with Realtime access. This is separate from Codex sign-in."
		if a.voiceAuth.Source == "environment_key" {
			detail = "Using the voice API key from the server environment."
		}
		ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c.Key("voice-api-key"), &a.voiceKeyDraft).Password().Label("Voice API key").
				Placeholder("OpenAI API key").Height(31).Grow(1).MinWidth(0)
			ui.PrimaryButton(c.Key("save-voice-key"), "Save key").Height(31).
				Disabled(a.voiceKeyBusy || !a.online || strings.TrimSpace(a.voiceKeyDraft) == "").
				OnClick(func() { a.changeVoiceKey(http.MethodPut) })
			if a.voiceAuth.Source == "saved_key" {
				ui.Button(c.Key("remove-voice-key"), "Remove").Height(31).Disabled(a.voiceKeyBusy || !a.online).
					OnClick(func() { a.changeVoiceKey(http.MethodDelete) })
			}
		})
		a.micCheckControls(c)
		if a.voiceKeyErr != "" {
			ui.Text(c, a.voiceKeyErr).FontSize(12).TextColor(c.Theme().Danger)
		}
	})
}

var (
	callCanvas   = ui.Hex("#16171C")
	callTile     = ui.Hex("#22242D")
	callEdge     = ui.Hex("#373946")
	callWhite    = ui.Hex("#F5F5F8")
	callDim      = ui.Hex("#B3B5C5")
	callAccent   = ui.Hex("#B9AEFF")
	callOnAccent = ui.Hex("#211943")
	callRed      = ui.Hex("#B93650")
	hangMark     = svg(`<path d="M3.5 15.4v-2.9c0-1.5 3.4-3.7 8.5-3.7s8.5 2.2 8.5 3.7v2.9h-4.7l-.9-2.7a10.4 10.4 0 0 0-5.8 0l-.9 2.7z"/>`)
	mutedMicMark = svg(`<path d="m4 4 16 16M9 9V6a3 3 0 0 1 6 0v6m-9-1v1a6 6 0 0 0 9 5m3-5v-1m-6 7v3m-3 0h6"/>`)
	inviteMark   = svg(`<circle cx="9" cy="8" r="3"/><path d="M3 20v-1a5 5 0 0 1 5-5h2a5 5 0 0 1 5 5v1m3-13v6m-3-3h6"/>`)
)

func (a *desktop) callView(c *ui.Context) {
	call := a.voice
	a.refreshWork(c, &call.work, call.chat)
	bar := c.TitleBar()
	ui.Column(c.Key("call-room")).Fill().Background(callCanvas).TextColor(callWhite).Children(func() {
		ui.Row(c).Height(max(48, bar.Height)).Padding(0, max(22, bar.Right+14), 0, max(22, bar.Left+16)).
			AlignItems(ui.Center).Gap(8).DragWindow().Children(func() {
			ui.Text(c, "Buddy").FontSize(16).FontWeight(730).TextColor(callWhite)
			ui.Text(c, "/  Voice call").FontSize(13).TextColor(callDim)
			ui.Box(c).Grow(1)
			if call.ready && len(call.members) > 1 {
				label := "Continue after call"
				if call.teamQueued {
					label = "Team queued"
				}
				callButton(c.Key("voice-continue"), label, buddyMark, callTile, callWhite).
					Tooltip("Have the team continue the discussion and post updates in this chat after you hang up").
					Disabled(call.teamQueued || call.teamPending || call.ending).OnClick(func() {
					if a.commandVoice(api.VoiceEvent{Type: "continue_chat"}) {
						call.teamPending = true
					}
				})
			}
			callButton(c.Key("call-back"), "Back to chat", buddyMark, callTile, callWhite).OnClick(func() { a.backToCallChat(call) })
		})
		ui.Column(c).Grow(1).MinHeight(0).FillWidth().MaxWidth(1060).AlignSelf(ui.Center).
			Padding(12, 26, 16).Gap(12).Children(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, call.title).FontSize(26).FontWeight(720).SingleLine().Role(ui.RoleHeading)
					status := "Calling…"
					if call.ending {
						status = "Ending call…"
					} else if call.joining != "" {
						status = "Inviting " + call.joining + "…"
					} else if call.ready {
						status = fmt.Sprintf("Live · %d in the call", len(call.members)+1)
					} else if call.openingAudio {
						status = "Connecting audio…"
					}
					ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
						color := callAccent
						if call.ready && !call.ending {
							color = ui.Hex("#8EE2BA")
						}
						ui.Box(c).Size(7, 7).Radius(4).Background(color)
						ui.Text(c, status).FontSize(13).TextColor(callDim).SingleLine()
						if lead := call.memberName(call.lead); call.ready && lead != "" && len(call.members) > 1 {
							ui.Text(c, "·  Lead: "+lead).FontSize(13).TextColor(callDim).MinWidth(0).SingleLine()
						}
					})
				})
				if call.ready {
					now := c.Now().Sub(call.started)
					if now < 0 {
						now = 0
					}
					ui.Text(c, fmt.Sprintf("%02d:%02d", int(now.Minutes()), int(now.Seconds())%60)).
						FontSize(13).TextColor(callDim)
					c.After(time.Second - now%time.Second)
					label := "Work"
					active := 0
					for _, job := range call.work.items {
						if job.Status.Active() {
							active++
						}
					}
					if active > 0 {
						label = fmt.Sprintf("Work · %d", active)
					}
					callButton(c.Key("call-work"), label, workMark, callTile, callWhite).OnClick(func() { call.workOpen = !call.workOpen })
				}
			})
			ui.Row(c).Grow(1).MinHeight(0).FillWidth().Gap(12).AlignItems(ui.Stretch).Children(func() {
				a.callStage(c, call)
				if call.workOpen {
					a.callWorkPanel(c, call)
				}
			})
			a.callCaption(c, call)
			ui.Row(c).FillWidth().Gap(10).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				invite := callButton(c.Key("voice-invite"), "Invite Buds", inviteMark, callTile, callWhite).
					Disabled(!call.ready || call.ending || call.joining != "" || len(call.members) >= 8)
				invite.PopoverMenuButton(func(menu *ui.Menu) {
					available := false
					for _, agent := range a.directory.Agents {
						if call.contains(agent.ID) {
							continue
						}
						available = true
						if menu.Item(agent.Name).Chosen() {
							if a.commandVoice(api.VoiceEvent{Type: "invite", Agent: agent.ID}) {
								call.joining, call.joiningID = agent.Name, agent.ID
							}
						}
					}
					if !available {
						menu.Item("No other Buds")
					}
				})
				label, mark := "Mute", micMark
				bg, color := callTile, callWhite
				if call.muted.Load() {
					label, mark, bg, color = "Unmute", mutedMicMark, callAccent, callOnAccent
				}
				callButton(c.Key("voice-mute"), label, mark, bg, color).MinWidth(112).
					Disabled(!call.ready || call.ending).OnClick(a.toggleVoiceMute)
				if call.directMic && call.speaking != "" {
					callButton(c.Key("voice-interrupt"), "Interrupt", stopMark, callTile, callWhite).
						OnClick(func() { a.commandVoice(api.VoiceEvent{Type: "interrupt"}) })
				}
				callButton(c.Key("voice-hangup"), "Hang up", hangMark, callRed, callWhite).
					Disabled(call.ending || call.teamPending).OnClick(a.endVoice)
			})
		})
	})
}

func callButton(c *ui.Context, label string, mark *ui.SVG, bg, fg ui.Color) ui.Element {
	button := ui.ButtonBase(c).Height(39).Padding(0, 13).Radius(10).Background(bg).TextColor(fg).
		Label(label).Tooltip(label).Shrink(0).Center()
	if button.Hovered() {
		button.Background(bg.Mix(fg, .11))
	}
	button.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, mark).Size(17, 17)
			ui.Text(c, label).FontSize(13).FontWeight(650).SingleLine()
		})
	})
	return button
}

func (a *desktop) callStage(c *ui.Context, call *desktopVoice) {
	count := len(call.members) + 1
	cols := callColumns(count)
	ui.Scroll(c.Key("call-stage")).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			for i := 0; i < count; i += cols {
				ui.Row(c.Key(i)).FillWidth().Gap(10).Children(func() {
					for j := i; j < min(i+cols, count); j++ {
						if j == len(call.members) {
							a.callSelf(c, call, count)
							continue
						}
						a.callAgentTile(c, call, call.members[j], count)
					}
					for range max(0, i+cols-count) {
						ui.Box(c).Grow(1).MinWidth(0)
					}
				})
			}
		})
	})
}

func (a *desktop) callAgentTile(c *ui.Context, call *desktopVoice, member api.VoiceMember, count int) {
	active := call.focus == member.ID
	speaking := call.speaking == member.ID
	status := "In the call"
	mood := orbIdle
	switch {
	case !call.ready:
		status, mood = "Connecting", orbMood(tasks.Listening)
	case speaking:
		status, mood = "Speaking", orbSpeaking
	case call.waiting == member.ID:
		status, mood = "About to speak", orbMood(tasks.Reasoning)
	case call.next == member.ID:
		status = "Up next"
	case call.hearing:
		status, mood = "Listening", orbMood(tasks.Listening)
	case active && len(call.members) == 1:
		status = "Ready"
	}
	edge := callEdge
	if speaking {
		edge = callAccent
	}
	lead := call.lead == member.ID && len(call.members) > 1
	label := member.Name + " · " + status
	if lead {
		label += " · Lead Bud"
	}
	key := c.Key("call-member-" + member.ID)
	var tile ui.Element
	if call.ready && !call.ending && len(call.members) > 1 {
		tile = ui.ButtonBase(key).Tooltip("Talk to " + member.Name).
			OnClick(func() { a.commandVoice(api.VoiceEvent{Type: "focus", Agent: member.ID}) })
	} else {
		tile = ui.Box(key).Role(ui.RoleGroup)
	}
	tile.Grow(1).MinWidth(0).Height(callTileHeight(c, count)).Radius(17).
		Background(callTile).Border(1.5, edge).Label(label).Center()
	if call.ready && !call.ending && len(call.members) > 1 && tile.Hovered() {
		tile.Background(callTile.Mix(callAccent, .07))
	}
	profile := call.profiles[member.ID]
	profile.ID, profile.Name = member.ID, member.Name
	size := callAvatarSize(c, call, count)
	tile.Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(5).MinWidth(0).Padding(8, 12).Children(func() {
			halo := ui.Hex("#A8ABCC")
			if speaking {
				halo = callAccent
			}
			ui.Box(c).Size(size+18, size+18).Radius((size+18)/2).Border(3, halo.Alpha(.57)).
				Background(ui.Hex("#DFDDEF")).Center().Children(func() {
				a.drawProfile(c, profile, size, speaking || mood != orbIdle, mood)
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, member.Name).FontSize(15).FontWeight(700).MinWidth(0).SingleLine().TextColor(callWhite)
				if lead {
					ui.Icon(c, primaryMark).Size(13, 13).Shrink(0).TextColor(callAccent)
				}
			})
			stateColor := callDim
			if speaking || active {
				stateColor = callAccent
			}
			ui.Text(c, status).FontSize(12).SingleLine().TextColor(stateColor)
		})
	})
}

func (a *desktop) callSelf(c *ui.Context, call *desktopVoice, count int) {
	muted := call.muted.Load()
	status := "Mic on"
	if muted {
		status = "Mic off"
	}
	if !muted && !call.micReady {
		status = call.micStatus
		if status == "" {
			status = "Connecting microphone…"
		}
	}
	size := callAvatarSize(c, call, count)
	ui.Column(c.Key("call-self")).Grow(1).MinWidth(0).Height(callTileHeight(c, count)).
		AlignItems(ui.Center).Justify(ui.Center).Gap(5).Radius(17).Background(callTile).Border(1, callEdge).Children(func() {
		ui.Column(c).Size(size+18, size+18).Radius((size + 18) / 2).Background(ui.Hex("#37334A")).
			AlignItems(ui.Center).Justify(ui.Center).Gap(8).Children(func() {
			mark := micMark
			if muted {
				mark = mutedMicMark
			}
			ui.Icon(c, mark).Size(size*.37, size*.37).TextColor(callAccent)
			ui.Row(c).Height(14).AlignItems(ui.Center).Gap(3).Children(func() {
				for i, gain := range []float32{.38, .65, 1, .73, .42} {
					level := call.micLevel
					if muted {
						level = 0
					}
					ui.Box(c.Key(i)).Size(3, 3+level*gain*11).Radius(2).Background(callAccent.Alpha(.35 + level*.65))
				}
			})
		})
		ui.Text(c, "You").FontSize(15).FontWeight(700).TextColor(callWhite)
		ui.Text(c, status).FontSize(12).TextColor(callDim)
	})
}

func callColumns(count int) int {
	if count == 2 || count == 4 {
		return 2
	}
	return 3
}

func callTileHeight(c *ui.Context, count int) float32 {
	_, height := c.Size()
	cols := callColumns(count)
	rows := (count + cols - 1) / cols
	// Leave room for the titlebar, call heading, captions, and controls.
	return max(150, min(420, (height-293-float32(rows-1)*10)/float32(rows)))
}

func callAvatarSize(c *ui.Context, call *desktopVoice, count int) float32 {
	width, _ := c.Size()
	width = min(width, 1060) - 52
	if call.workOpen {
		width -= 318
	}
	cols := float32(callColumns(count))
	tile := (width - 10*(cols-1)) / cols
	return max(40, min(146, callTileHeight(c, count)*.45, tile-42))
}

func (a *desktop) callCaption(c *ui.Context, call *desktopVoice) {
	ui.Column(c.Key("call-caption")).Height(62).FillWidth().Padding(7, 13).Gap(4).
		Radius(10).Background(callTile).Border(1, callEdge).Children(func() {
		head, text := "Live captions", "Ready when you are."
		if !call.ready {
			text = "Connecting your microphone and speaker…"
		}
		if call.hearing && len(call.members) > 1 {
			text = "Everyone is listening…"
		} else if call.hearing {
			text = "Listening…"
		} else if call.routing {
			text = "Choosing who should answer…"
		}
		if call.speaker != "" && call.caption != "" {
			head, text = call.speaker, call.caption
		}
		if call.notice != "" {
			head, text = "Call", call.notice
		}
		ui.Text(c, head).FontSize(11).FontWeight(650).TextColor(callAccent).SingleLine()
		ui.Text(c, text).FontSize(13).TextColor(callWhite).MaxLines(2).Tooltip(text)
	})
}

func (a *desktop) voicePanel(c *ui.Context) {
	call := a.voice
	if call == nil {
		if a.voiceErr != "" && a.section == "" {
			ui.Row(c.Key("voice-error")).Padding(4, 22).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Text(c, a.voiceErr).FontSize(12).TextColor(c.Theme().Danger).Grow(1).MinWidth(0)
				ui.Button(c.Key("dismiss-voice-error"), "Dismiss").OnClick(func() { a.voiceErr = "" })
			})
		}
		return
	}
	ui.Row(c.Key("voice-panel")).Padding(3, 20, 7).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Icon(c, callMark).Size(15, 15).TextColor(c.Theme().Text)
		status := "Calling " + call.title + "…"
		if call.ready {
			status = "In a call · " + call.title
		}
		ui.Text(c, status).FontSize(13).FontWeight(650).SingleLine().Grow(1).MinWidth(0)
		ui.Button(c.Key("voice-reopen"), "Return to call").OnClick(func() { call.expanded = true })
		ui.Button(c.Key("voice-mini-hangup"), "Hang up").TextColor(c.Theme().Danger).Disabled(call.ending || call.teamPending).OnClick(a.endVoice)
	})
}
