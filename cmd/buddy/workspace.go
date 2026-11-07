package main

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func (a *desktop) currentConversation() string {
	if a.conversationID != "" {
		return a.conversationID
	}
	return tasks.DefaultConversationID
}

func groupHasBud(chat tasks.Conversation, id string) bool {
	return slices.ContainsFunc(chat.Members, func(bud tasks.AgentRef) bool { return bud.ID == id })
}

func resolveChatSelection(chat tasks.Conversation, inspected string) (string, string) {
	if chat.Name == "" {
		return "", chat.Agent.ID
	}
	if !groupHasBud(chat, inspected) {
		inspected = ""
	}
	workspace := inspected
	if workspace == "" {
		workspace = chat.Agent.ID
	}
	return inspected, workspace
}

func (a *desktop) openConversation(id string) {
	if a.sending || a.saving {
		return
	}
	a.section = ""
	a.conversationTab = conversationChatTab
	if id == a.currentConversation() {
		a.conversationID = id
		a.refreshSoon()
		return
	}
	if a.drafts == nil {
		a.drafts = make(map[string]string)
	}
	a.drafts[a.currentConversation()] = a.draft
	if a.attachmentDraftsByChat == nil {
		a.attachmentDraftsByChat = make(map[string][]tasks.Attachment)
	}
	a.attachmentDraftsByChat[a.currentConversation()] = a.attachments
	a.attachments = a.attachmentDraftsByChat[id]
	delete(a.attachmentDraftsByChat, id)
	a.dictationPending = time.Time{}
	a.skillCaretPending = false
	a.conversationID, a.draft, a.inspectedAgent = id, a.drafts[id], ""
	a.chat, a.workspace = tasks.Conversation{}, tasks.Workspace{}
	a.list = ui.ListState{}
	a.chatViewport = chatViewport{}
	a.loadingHistory = false
	a.chatErr, a.historyErr, a.workspaceErr, a.inspectorErr, a.retryKey = "", "", "", "", ""
	a.refreshSoon()
}

func (a *desktop) agentProfile(id string) tasks.Agent {
	for _, profile := range a.chat.Profiles {
		if profile.ID == id {
			return profile
		}
	}
	return a.chat.Agent
}

func (a *desktop) replyProfile(id string) tasks.Agent {
	profile := a.agentProfile(id)
	if id == "" || profile.ID == id {
		return profile
	}
	for _, bud := range a.directory.RemovedAgents {
		if bud.ID == id {
			return a.directoryProfile(bud)
		}
	}
	return tasks.Agent{ID: id}
}

func (a *desktop) focusedAgent() tasks.Agent { return a.chat.Agent }

func (a *desktop) inspectorAgent() tasks.Agent {
	if a.inspectedAgent != "" {
		return a.agentProfile(a.inspectedAgent)
	}
	return a.focusedAgent()
}

func (a *desktop) inspectAgent(id string) {
	if a.inspectedAgent != id {
		a.inspectedAgent = id
		a.workspace, a.workspaceErr = tasks.Workspace{}, ""
		a.refreshSoon()
	}
}

func (a *desktop) canSteer(id string) bool {
	if active := a.chat.Active; active != nil && active.ID == id {
		return active.CanSteer
	}
	for _, work := range a.chat.Work {
		if work.ID == id {
			return work.CanSteer
		}
	}
	return false
}

func (a *desktop) startAgent() {
	if a.saving || a.sending {
		return
	}
	a.editReturn = ""
	if a.section == "buds" {
		a.editReturn = "buds"
	}
	a.section, a.creatingAgent, a.editErr, a.picturePicking = "agent", true, "", false
	a.editLoading = false
	a.edit = copyAgent(tasks.Agent{AllowEdits: true, Model: tasks.DefaultCodexModel})
	a.loadModels()
}

func (a *desktop) startGroup() {
	if a.saving || a.sending {
		return
	}
	a.section, a.editErr = "group", ""
	a.group = tasks.GroupRequest{}
}

func (a *desktop) createGroup() {
	if a.saving || !a.online || len(a.group.Members) < 2 {
		return
	}
	a.saving, a.editErr = true, ""
	win, group := a.window, a.group
	group.Members = append([]string(nil), group.Members...)
	if a.canSetLeadership() {
		group.LeadAgentID = a.draftGroupLead()
	}
	go func() {
		created, err := a.client.CreateGroup(a.ctx, group)
		win.Update(func() {
			a.saving = false
			if err != nil {
				a.editErr = errorMessage(err, "Couldn't create the group chat.")
				return
			}
			a.openConversation(created.ID)
		})
	}()
}

func (a *desktop) startRoutine() {
	if a.saving {
		return
	}
	a.section, a.editErr = "routine", ""
	a.routine = tasks.Routine{AgentID: a.inspectorAgent().ID}
	a.routineCadence, a.routineInterval = 1440, "30"
}

func (a *desktop) routineMinutes() (int, bool) {
	minutes := a.routineCadence
	if minutes == 0 {
		var err error
		minutes, err = strconv.Atoi(strings.TrimSpace(a.routineInterval))
		if err != nil {
			return 0, false
		}
	}
	return minutes, minutes >= tasks.MinRoutineMinutes && minutes <= tasks.MaxRoutineMinutes
}

func (a *desktop) createRoutine() {
	if a.saving || !a.online {
		return
	}
	minutes, valid := a.routineMinutes()
	if !valid {
		a.editErr = "Enter a whole number from 1 to 10,080."
		return
	}
	a.saving, a.editErr = true, ""
	win, id, routine := a.window, a.currentConversation(), a.routine
	routine.Minutes = minutes
	go func() {
		err := a.client.AddRoutine(a.ctx, id, routine)
		win.Update(func() {
			a.saving = false
			if err != nil {
				a.editErr = errorMessage(err, "Couldn't schedule the routine.")
				return
			}
			a.section = ""
			a.refreshSoon()
		})
	}()
}

func (a *desktop) changeRoutine(id string, enabled *bool) {
	if !a.online {
		return
	}
	win, chat := a.window, a.currentConversation()
	go func() {
		err := a.client.UpdateRoutine(a.ctx, chat, id, enabled)
		win.Update(func() {
			if a.currentConversation() != chat {
				return
			}
			a.inspectorErr = ""
			if err != nil {
				a.inspectorErr = errorMessage(err, "Couldn't update the routine.")
			}
			a.refreshSoon()
		})
	}()
}
