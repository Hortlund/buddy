package main

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// MyGo's main thread owns the view state. Network and dialog results are
// applied through Window.Update.
type desktop struct {
	execution                                          executionView
	history                                            historyView
	historyChanging                                    bool
	historyNotice                                      ui.Toast
	mobile                                             mobileView
	ctx                                                context.Context
	client                                             *api.Client
	window                                             *mygo.Window
	endpoint                                           string
	initialRepo                                        string
	theme                                              string
	themeFile                                          string
	preferencesErr                                     string
	languages                                          tasks.LanguagePreferences
	loadedLanguages, loadingLanguages, savingLanguages bool

	section                  string
	chat                     tasks.Conversation
	edit                     tasks.Agent
	draft                    string
	attachments              []tasks.Attachment
	attachmentDraftsByChat   map[string][]tasks.Attachment
	composerInput            ui.Handle
	focusComposer            bool
	work                     workBrowser
	files                    map[string]*workspaceFilesPane
	visibleFiles             *workspaceFilesPane
	referenceRequest         uint64
	workspaceClose           workspaceCloseState
	conversationTab          int
	confirmingFileDiscard    bool
	autonomy                 autonomyView
	integrations             integrationsView
	mcp                      mcpView
	skillBusy                bool
	skillNotice              string
	skillCaretPending        bool
	selectedSkills           map[string]string
	creatingProposal         string
	dictationPending         time.Time
	pickingFiles, savingFile bool
	reactingMessage          int64
	list                     ui.ListState
	profileAvatar            avatarCache
	editAvatar               avatarCache
	picturePicking           bool
	models                   []tasks.Model
	modelsLoading            bool
	modelsLoaded             bool
	modelsRevision           uint64
	auth                     api.CodexAuth
	authDraft                string
	authErr                  string
	authBusy                 bool

	loadingHistory  bool
	historyErr      string
	revision        uint64
	refreshing      bool
	nextPoll        time.Time
	online          bool
	serverProtocol  int
	protocolChecked time.Time
	loaded          bool
	serverErr       error
	chatErr         string
	editErr         string
	sending         bool
	saving          bool
	browsing        bool
	stopping        string
	retryKey        string
	retryText       string
	retryTarget     string
	retryJob        string
	retrySkill      string

	conversationID  string
	inspectedAgent  string
	directory       tasks.Directory
	workspace       tasks.Workspace
	workspaceErr    string
	inspectorClosed bool
	otherAvatars    map[string]*avatarCache
	drafts          map[string]string
	jobDrafts       map[string]tasks.WorkItem
	chatReads       map[string]chatReadProgress
	chatViewport    chatViewport
	creatingAgent   bool
	editLoading     bool
	editReturn      string
	budPictures     map[string]*budPicture
	budSettingsErr  string
	primarySaving   string
	removingBud     string
	showRemovedBuds bool
	groupLeadSaving bool
	group           tasks.GroupRequest
	routine         tasks.Routine
	routineInterval string
	routineCadence  int
	inspectorErr    string
	answeringCard   string
	hostFile        string
	localServer     *localServer
	onboarding      bool
	hostGuideOpen   bool
	hasSavedHost    bool
	canRelaunchHost bool
	remoteHost      bool
	hostBusy        bool
	hostPending     bool
	hostNotice      string
	devices         []tasks.Device
	deviceErr       string
	pairOpen        bool
	pairName        string
	pairAddress     string
	pairFilename    string
	pairNotice      string
	moveOpen        bool
	moveAddress     string
	moveListen      string
	moveExporting   bool
	moveArchive     string
	movedTo         string
	moveNotice      string
	moveSourceDir   string
	removingDevice  string
	deviceNotice    string
	pairID          string
	pairSameHost    bool
	pairTunnel      bool
	pairSSH         string
	pairWorkspace   string
	pairPool        bool
	pairEdits       bool
	authOpen        bool
	voice           *desktopVoice
	voiceErr        string
	directVoiceMic  bool
	micCheckCancel  context.CancelFunc
	micCheckLevel   float32
	micCheckStatus  string
	voiceAuth       api.VoiceAuth
	voiceAuthLoaded bool
	voiceKeyOpen    bool
	voiceKeyBusy    bool
	voiceKeyDraft   string
	voiceKeyErr     string
}

func (a *desktop) refresh(c *ui.Context) {
	if a.window == nil || a.refreshing {
		return
	}
	if remaining := time.Until(a.nextPoll); remaining > 0 {
		c.After(remaining)
		return
	}
	a.refreshing = true
	win, revision, id := a.window, a.revision, a.currentConversation()
	reactionsAfter := int64(-1)
	if a.chat.ID == id {
		reactionsAfter = a.chat.ReactionRevision
	}
	startup := a.conversationID == "" && a.section == ""
	inChat := a.section == "" && !a.onboarding
	inspected := a.inspectedAgent
	inspect := inChat && !a.inspectorClosed
	connections := a.section == "connections"
	checkProtocol := time.Since(a.protocolChecked) >= 15*time.Second
	go func() {
		var directory tasks.Directory
		var directoryErr error
		if startup {
			directory, directoryErr = a.client.Directory(a.ctx)
			if directoryErr == nil {
				id = primaryConversation(directory)
			}
		}
		chat, err := a.client.ConversationWithReactions(a.ctx, id, 0, reactionsAfter)
		var protocol int
		var protocolErr error
		if err == nil && checkProtocol {
			protocol, protocolErr = a.client.DesktopProtocol(a.ctx)
		}
		var workspace tasks.Workspace
		var workspaceErr string
		var devices []tasks.Device
		var devicesErr error
		if err == nil {
			var workspaceBud string
			inspected, workspaceBud = resolveChatSelection(chat, inspected)
			if !startup {
				directory, directoryErr = a.client.Directory(a.ctx)
			}
			if connections {
				devices, devicesErr = a.client.Devices(a.ctx)
			}
			if inspect {
				var fileErr error
				workspace, fileErr = a.client.Workspace(a.ctx, workspaceBud)
				if fileErr != nil {
					workspaceErr = errorMessage(fileErr, "Couldn't read workspace files.")
				}
			}
		}
		win.Update(func() {
			a.refreshing = false
			if a.revision != revision {
				return
			}
			a.loaded, a.online, a.serverErr = true, err == nil, err
			if err != nil {
				a.protocolChecked = time.Time{}
				a.nextPoll = time.Now().Add(2 * time.Second)
				return
			}
			if checkProtocol && protocolErr == nil {
				a.serverProtocol, a.protocolChecked = protocol, time.Now()
			}
			if startup && directoryErr == nil {
				a.conversationID = id
			}
			if chat.TranscriptRevision != a.chat.TranscriptRevision {
				a.chatViewport, a.list = chatViewport{}, ui.ListState{}
			} else if len(a.chat.Messages) != 0 && len(chat.Messages) != 0 &&
				chat.Messages[0].ID <= a.chat.Messages[len(a.chat.Messages)-1].ID+1 {
				chat.Before = a.chat.Before
				chat.Messages = mergeMessages(a.chat.Messages, chat.Messages)
			}
			if len(chat.EarlierUpdates) != 0 {
				chat.Messages = mergeMessages(chat.Messages, chat.EarlierUpdates)
			}
			applyReactionUpdates(chat.Messages, chat.ReactionUpdates)
			a.chat = chat
			a.inspectedAgent = inspected
			if inspect {
				a.workspace, a.workspaceErr = workspace, workspaceErr
			}
			if (connections || a.onboarding) && a.auth.State == "" && !a.authBusy && a.authErr == "" {
				a.changeAuth("read")
			}
			if connections {
				a.deviceErr = ""
				if devicesErr == nil {
					a.devices = devices
				} else {
					a.deviceErr = errorMessage(devicesErr, "Couldn't load worker devices.")
				}
			}
			if directoryErr == nil {
				a.directory = directory
				a.syncBudPictures()
			}
			a.nextPoll = time.Now().Add(3 * time.Second)
			if chat.Active != nil {
				a.nextPoll = time.Now().Add(500 * time.Millisecond)
				return
			}
			for _, work := range chat.Work {
				if work.Status.Active() {
					a.nextPoll = time.Now().Add(time.Second)
					return
				}
			}
			for _, message := range chat.Messages {
				if message.Status.Active() {
					a.nextPoll = time.Now().Add(time.Second)
					break
				}
			}
		})
	}()
}

func mergeMessages(existing, incoming []tasks.Message) []tasks.Message {
	byID := make(map[int64]tasks.Message, len(existing)+len(incoming))
	for _, message := range existing {
		byID[message.ID] = message
	}
	for _, message := range incoming {
		byID[message.ID] = message
	}
	merged := make([]tasks.Message, 0, len(byID))
	for _, message := range byID {
		merged = append(merged, message)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	return merged
}

func (a *desktop) loadEarlier() {
	if a.loadingHistory || !a.online || a.chat.Before == 0 || a.window == nil {
		return
	}
	a.loadingHistory, a.historyErr = true, ""
	win, before, id := a.window, a.chat.Before, a.currentConversation()
	go func() {
		page, err := a.client.Conversation(a.ctx, id, before)
		win.Update(func() {
			if a.currentConversation() != id {
				return
			}
			a.loadingHistory = false
			if a.chat.Before != before || err == nil && page.TranscriptRevision != a.chat.TranscriptRevision {
				return
			}
			if err != nil {
				a.historyErr = errorMessage(err, "Couldn't load earlier messages. Try again.")
				return
			}
			a.chat.Messages = mergeMessages(page.Messages, a.chat.Messages)
			a.chat.Before = page.Before
			if page.ReactionRevision < a.chat.ReactionRevision {
				a.chat.ReactionRevision = page.ReactionRevision
				a.refreshSoon()
			}
			a.list.ScrollTo(0, ui.Start)
		})
	}()
}

func (a *desktop) refreshSoon() {
	a.revision++
	a.nextPoll = time.Time{}
}

func (a *desktop) agentName() string {
	if profile := a.focusedAgent(); profile.Name != "" {
		return profile.Name
	}
	return "Buddy"
}

func (a *desktop) showSettings() { a.showSettingsFor(a.focusedAgent()) }

func (a *desktop) showSettingsFor(profile tasks.Agent) {
	if a.saving {
		return
	}
	if a.section != "agent" {
		a.edit = copyAgent(profile)
		a.creatingAgent, a.editLoading, a.editReturn = false, false, ""
		a.editErr = ""
		a.picturePicking = false
	}
	a.section = "agent"
	a.loadModels()
}

func (a *desktop) browse() {
	if a.browsing || a.window == nil || a.remoteHost {
		return
	}
	a.browsing = true
	win, path, draft := a.window, a.edit.Workspace, a.edit.Avatar
	if path == "" {
		path = a.initialRepo
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: "Choose a working folder", DefaultPath: path,
			ButtonLabel: "Choose Folder", Directory: true,
		})
		win.Update(func() {
			a.browsing = false
			if a.section != "agent" || a.edit.Avatar != draft {
				return
			}
			if err != nil {
				a.editErr = "Couldn't open the folder picker."
			} else if len(paths) != 0 {
				a.edit.Workspace, a.editErr = paths[0], ""
			}
		})
	}()
}

func (a *desktop) saveAgent() {
	if a.saving || !a.online || a.window == nil {
		return
	}
	a.saving, a.editErr = true, ""
	win, profile, creating, back := a.window, copyAgent(a.edit), a.creatingAgent, a.editReturn
	profile.ConfirmWorkspace = profile.WorkspaceNeedsReview
	if path := profile.Workspace; !a.remoteHost && strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			profile.Workspace = filepath.Join(home, path[2:])
		}
	}
	go func() {
		var saved tasks.Agent
		var err error
		if creating {
			saved, err = a.client.CreateAgent(a.ctx, profile)
		} else {
			saved, err = a.client.SaveAgent(a.ctx, profile)
		}
		win.Update(func() {
			a.saving = false
			if err != nil {
				a.editErr = errorMessage(err, "Couldn't save this Bud. Try again.")
				return
			}
			a.edit = copyAgent(saved)
			a.section = back
			if creating && back == "" {
				a.openConversation(saved.ConversationID)
			} else {
				a.refreshSoon()
			}
		})
	}()
}

func copyAgent(agent tasks.Agent) tasks.Agent {
	look := avatarAppearance(agent)
	agent.Avatar = &look
	return agent
}

func (a *desktop) send() { a.submit("") }

func (a *desktop) canSendAttachments() bool {
	return a.protocolChecked.IsZero() || a.serverProtocol >= api.AttachmentProtocol
}

func (a *desktop) submit(target string) {
	if a.chat.Archived || a.chat.Agent.Removed {
		return
	}
	id := a.currentConversation()
	job := a.jobDrafts[id]
	text := strings.TrimSpace(a.draft)
	skill := a.skillForDraft(text)
	if skill != "" {
		target = ""
	}
	if target != "" && (!a.canSteer(target) || len(a.attachments) != 0 || job.ID != "") {
		return
	}
	if (text == "" && len(a.attachments) == 0) || a.sending || a.pickingFiles || !a.online || a.window == nil {
		return
	}
	if len(a.attachments) != 0 && !a.canSendAttachments() {
		a.chatErr = "Update the Buddy server to send attachments."
		return
	}
	if skill != "" && job.InputID != "" {
		a.chatErr = "Remove the job reply reference to run a skill in chat."
		return
	}
	if a.serverProtocol < api.SkillSelectionProtocol {
		if skill != "" && a.selectedSkills[id] == skill {
			a.chatErr = "Update the Buddy server to send the selected skill."
			return
		}
		skill = ""
	}
	a.sending, a.chatErr = true, ""
	jobKey := job.ID + "/" + job.InputID
	if a.retryKey == "" || a.retryText != text || a.retryTarget != target || a.retryJob != jobKey || a.retrySkill != skill {
		a.retryKey, a.retryText, a.retryTarget, a.retryJob, a.retrySkill = rand.Text(), text, target, jobKey, skill
	}
	files := slices.Clone(a.attachments)
	win, key := a.window, a.retryKey
	go func() {
		var err error
		switch {
		case job.InputID != "":
			err = a.client.AnswerWorkInput(a.ctx, id, job.InputID, text, key, files)
		case target != "":
			err = a.client.Steer(a.ctx, target, text, key)
		default:
			_, err = a.client.SendMessage(a.ctx, id, key, tasks.MessageRequest{
				Text: text, Attachments: files, JobID: job.ID, SkillName: skill,
			})
		}
		win.Update(func() {
			a.sending = false
			if err != nil {
				a.chatErr = errorMessage(err, "Couldn't confirm the send. Check the chat or retry.")
				var remote *api.HTTPError
				if errors.As(err, &remote) && remote.Status < 500 {
					a.retryKey = ""
				}
				return
			}
			a.setAttachments(nil)
			delete(a.jobDrafts, id)
			if strings.TrimSpace(a.draft) == text {
				a.draft = ""
				delete(a.selectedSkills, id)
			}
			a.refreshSoon()
			a.list.ScrollToEnd()
		})
	}()
}

func (a *desktop) cancelTurn(id string, queuedOnly bool) {
	chat := a.currentConversation()
	if a.stopTask(id, queuedOnly, func(_ tasks.Task, err error) {
		if a.currentConversation() == chat && err != nil {
			a.chatErr = errorMessage(err, "Couldn't stop the request. Try again.")
		}
	}) {
		a.chatErr = ""
	}
}

func (a *desktop) stopTask(id string, queuedOnly bool, done func(tasks.Task, error)) bool {
	if a.stopping != "" || !a.online || a.window == nil {
		return false
	}
	a.stopping = id
	win, client := a.window, a.client
	go func() {
		task, err := client.Cancel(a.ctx, id, queuedOnly)
		win.Update(func() {
			a.stopping = ""
			if err == nil && task.Kind == tasks.BackgroundWork {
				a.work.noteStopped(task)
				if a.voice != nil {
					a.voice.work.noteStopped(task)
				}
			}
			done(task, err)
			a.refreshSoon()
		})
	}()
	return true
}

func errorMessage(err error, fallback string) string {
	var remote *api.HTTPError
	if errors.As(err, &remote) {
		return remote.Message
	}
	return fallback
}
