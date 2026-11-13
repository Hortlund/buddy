package main

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type budPicture struct {
	revision string
	image    []byte
}

var primaryMark = svg(`<path d="m12 3 2.5 5.6 6 .6-4.5 4 1.3 6-5.3-3.1-5.3 3.1 1.3-6-4.5-4 6-.6L12 3Z" fill="currentColor" stroke="none"/>`)

func budRole(role string) string {
	if strings.TrimSpace(role) == "" {
		return "Personal companion"
	}
	return role
}

func primaryConversation(directory tasks.Directory) string {
	for _, bud := range directory.Agents {
		if bud.ID == directory.PrimaryAgentID && bud.ConversationID != "" {
			return bud.ConversationID
		}
	}
	return tasks.DefaultConversationID
}

func (a *desktop) syncBudPictures() {
	if a.budPictures == nil {
		a.budPictures = make(map[string]*budPicture)
	}
	client, win := a.client, a.window
	for _, bud := range slices.Concat(a.directory.Agents, a.directory.RemovedAgents) {
		if bud.PictureRevision == "" {
			delete(a.budPictures, bud.ID)
			continue
		}
		if picture := a.budPictures[bud.ID]; picture != nil && picture.revision == bud.PictureRevision {
			continue
		}
		picture := &budPicture{revision: bud.PictureRevision}
		a.budPictures[bud.ID] = picture
		go func() {
			profile, err := client.Agent(a.ctx, bud.ID)
			image := avatarAppearance(profile).Image
			digest := sha256.Sum256(image)
			win.Update(func() {
				if a.budPictures[bud.ID] != picture {
					return
				}
				if err != nil || hex.EncodeToString(digest[:]) != picture.revision {
					delete(a.budPictures, bud.ID)
					return
				}
				picture.image = image
			})
		}()
	}
}

func (a *desktop) directoryProfile(ref tasks.AgentRef) tasks.Agent {
	if ref.Avatar == nil {
		if profile := a.agentProfile(ref.ID); profile.ID == ref.ID {
			return profile
		}
	}
	look := tasks.DefaultAvatar()
	if ref.Avatar != nil {
		look = *ref.Avatar
	}
	if picture := a.budPictures[ref.ID]; picture != nil && picture.revision == ref.PictureRevision {
		look.Image = picture.image
	}
	return tasks.Agent{ID: ref.ID, Name: ref.Name, Role: ref.Role, Avatar: &look}
}

func (a *desktop) showBudSettings() {
	if a.saving {
		return
	}
	a.section, a.budSettingsErr = "buds", ""
	a.refreshSoon()
}

func (a *desktop) editBud(id string) {
	if a.saving || !a.online || a.window == nil {
		return
	}
	a.section, a.editReturn = "agent", "buds"
	a.edit, a.creatingAgent = copyAgent(tasks.Agent{}), false
	a.editLoading, a.picturePicking, a.editErr = true, false, ""
	client, win, draft := a.client, a.window, a.edit.Avatar
	go func() {
		profile, err := client.Agent(a.ctx, id)
		win.Update(func() {
			if a.section != "agent" || a.edit.Avatar != draft {
				return
			}
			a.editLoading = false
			if err != nil {
				a.editErr = errorMessage(err, "Couldn't load this Bud's settings.")
				return
			}
			a.edit = copyAgent(profile)
			a.loadModels()
		})
	}()
}

func (a *desktop) canSetLeadership() bool {
	return a.online && (a.protocolChecked.IsZero() || a.serverProtocol >= api.BudSettingsProtocol)
}

func (a *desktop) setPrimaryBud(id string) {
	if !a.canSetLeadership() || a.primarySaving != "" || a.window == nil {
		return
	}
	a.primarySaving, a.budSettingsErr = id, ""
	client, win := a.client, a.window
	go func() {
		err := client.SetPrimaryAgent(a.ctx, id)
		win.Update(func() {
			a.primarySaving = ""
			if err != nil {
				a.budSettingsErr = errorMessage(err, "Couldn't change your Primary Bud.")
				return
			}
			a.directory.PrimaryAgentID = id
			buds := a.directory.Agents
			if i := slices.IndexFunc(buds, func(bud tasks.AgentRef) bool { return bud.ID == id }); i > 0 {
				bud := buds[i]
				copy(buds[1:i+1], buds[:i])
				buds[0] = bud
			}
			a.refreshSoon()
		})
	}()
}

func (a *desktop) canRemoveBud() bool {
	return a.online && a.serverProtocol >= api.RemoveBudProtocol && a.removingBud == "" &&
		len(a.directory.Agents) > 1 && a.primarySaving == "" && a.voice == nil
}

func (a *desktop) budWasRemoved(id string) bool {
	return slices.ContainsFunc(a.directory.RemovedAgents, func(bud tasks.AgentRef) bool { return bud.ID == id })
}

func (a *desktop) removeBud(bud tasks.AgentRef) {
	if !a.canRemoveBud() || a.window == nil {
		return
	}
	a.removingBud, a.budSettingsErr = bud.ID, ""
	win, client := a.window, a.client
	go func() {
		choice, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "Remove Bud",
			Message: "Remove " + bud.Name + "?",
			Detail: "This removes the Bud from active chats and stops its routines. " +
				"Chat history and workspace files are kept.",
			Buttons: []string{"Cancel", "Remove Bud"}, DestructiveButtons: []int{1},
		})
		confirmed := err == nil && choice.Button == 1
		if confirmed {
			err = client.RemoveAgent(a.ctx, bud.ID)
		}
		win.Update(func() {
			a.removingBud = ""
			if err != nil {
				a.budSettingsErr = errorMessage(err, "Couldn't remove this Bud.")
			} else if confirmed {
				a.refreshSoon()
			}
		})
	}()
}

func (a *desktop) setGroupLead(id string) {
	if !a.canSetLeadership() || a.groupLeadSaving || a.window == nil || a.chat.Name == "" || id == a.chat.Agent.ID {
		return
	}
	a.groupLeadSaving, a.chatErr = true, ""
	client, win, chat := a.client, a.window, a.currentConversation()
	go func() {
		err := client.SetGroupLead(a.ctx, chat, id)
		win.Update(func() {
			a.groupLeadSaving = false
			if a.currentConversation() != chat {
				return
			}
			if err != nil {
				a.chatErr = errorMessage(err, "Couldn't change the Lead Bud.")
				return
			}
			for i, profile := range a.chat.Profiles {
				if profile.ID == id {
					a.chat.Agent, a.chat.Profiles[i] = profile, a.chat.Agent
					break
				}
			}
			if a.inspectedAgent == "" {
				a.workspace, a.workspaceErr = tasks.Workspace{}, ""
			}
			a.refreshSoon()
		})
	}()
}

func (a *desktop) draftGroupLead() string {
	if len(a.group.Members) == 0 {
		return ""
	}
	if a.canSetLeadership() {
		if slices.Contains(a.group.Members, a.group.LeadAgentID) {
			return a.group.LeadAgentID
		}
		if slices.Contains(a.group.Members, a.directory.PrimaryAgentID) {
			return a.directory.PrimaryAgentID
		}
	}
	return a.group.Members[0]
}

func primaryBadge(c *ui.Context) {
	ui.Text(c, "Primary").FontSize(11).SingleLine().Shrink(0).TextColor(c.Theme().TextMuted)
}

func (a *desktop) budSettings(c *ui.Context) {
	controlPage(c.Key("buds-settings"), "Bud settings", "Manage your Buds and choose which one Buddy opens first.", nil, func() {
		if a.budSettingsErr != "" {
			ui.Text(c, a.budSettingsErr).FontSize(12).TextColor(c.Theme().Danger)
		}
		controlSection(c, "Your Buds", "", func() {
			ui.PrimaryButton(c.Key("settings-new-bud"), "New Bud").Shrink(0).Height(32).
				Disabled(!a.online || a.saving).OnClick(a.startAgent)
		})
		if len(a.directory.Agents) == 0 {
			controlEmpty(c, agentMark, "No Buds available", "Connect to the server to see your Buds.")
			return
		}
		connectionSection(c, func() {
			for _, bud := range a.directory.Agents {
				a.budSettingsRow(c.Key(bud.ID), bud)
			}
		})
		if len(a.directory.RemovedAgents) != 0 {
			ui.Button(c.Key("removed-buds"), "Removed Buds").AlignSelf(ui.Start).
				OnClick(func() { a.showRemovedBuds = !a.showRemovedBuds })
			if a.showRemovedBuds {
				connectionSection(c.Key("removed-bud-list"), func() {
					for _, bud := range a.directory.RemovedAgents {
						ui.Row(c.Key(bud.ID)).Gap(8).Padding(8, 12).AlignItems(ui.Center).Children(func() {
							ui.Text(c, bud.Name).Grow(1).MinWidth(0).SingleLine().FontWeight(650)
							ui.Button(c.Key("open-removed-"+bud.ID), "Open chat").Label("Open " + bud.Name + "’s chat").
								OnClick(func() { a.openConversation(bud.ConversationID) })
						})
					}
				})
			}
		}
	})
}

func (a *desktop) budSettingsRow(c *ui.Context, bud tasks.AgentRef) {
	primary := bud.ID == a.directory.PrimaryAgentID
	ui.Row(c).FillWidth().Padding(5, 12, 5, 7).Gap(7).AlignItems(ui.Center).Children(func() {
		button := ui.ButtonBase(c.Key("edit-bud-"+bud.ID)).Label("Edit "+bud.Name).
			Grow(1).MinWidth(0).Height(48).Padding(0, 6).Radius(8).Justify(ui.Start).
			Disabled(!a.online || a.saving).OnClick(func() { a.editBud(bud.ID) })
		if button.Hovered() {
			button.Background(c.Theme().SurfaceHover)
		}
		button.Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				a.drawProfile(c, a.directoryProfile(bud), 35, false, orbIdle)
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, bud.Name).FontSize(14).FontWeight(650).SingleLine()
					ui.Text(c, budRole(bud.Role)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
				})
				ui.Icon(c, arrowMark).Size(15, 15).TextColor(c.Theme().TextMuted).Shrink(0)
			})
		})
		if primary {
			primaryBadge(c)
		} else {
			label := "Make primary"
			if a.primarySaving == bud.ID {
				label = "Saving…"
			}
			ui.Button(c.Key("primary-bud-"+bud.ID), label).Label(label + " · " + bud.Name).Height(30).Shrink(0).
				Disabled(!a.canSetLeadership() || a.primarySaving != "" || a.removingBud != "").OnClick(func() { a.setPrimaryBud(bud.ID) })
		}
		if a.serverProtocol >= api.RemoveBudProtocol {
			label, help := "Remove", "Remove "+bud.Name
			if a.removingBud == bud.ID {
				label = "Removing…"
			} else if len(a.directory.Agents) == 1 {
				help = "Create another Bud before removing this one."
			} else if a.voice != nil {
				help = "End the call before removing a Bud."
			}
			ui.Button(c.Key("remove-bud-"+bud.ID), label).Label(label + " · " + bud.Name).Tooltip(help).
				Height(30).Shrink(0).TextColor(c.Theme().Danger).Disabled(!a.canRemoveBud()).
				OnClick(func() { a.removeBud(bud) })
		}
	})
}
