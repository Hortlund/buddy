package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"path"
	"path/filepath"
	"strings"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const (
	conversationChatTab = iota
	conversationFilesTab
)

type workspaceFilesPane struct {
	app           *desktop
	profile       tasks.Agent
	relative      string
	item          tasks.WorkspaceItem
	source        editor.State
	image         *ui.Bitmap
	request       uint64
	cancelLoad    context.CancelFunc
	preview       bool
	loading       bool
	saving        bool
	refreshOnShow bool
	status        string
	err           string
}

type workspaceCloseState struct {
	afterSave func()
	waitShown bool
	failed    string
}

func (a *desktop) currentWorkspaceFiles() *workspaceFilesPane {
	return a.files[a.currentConversation()]
}

func (a *desktop) showingWorkspaceFiles() bool {
	return a.section == "" && a.conversationTab == conversationFilesTab && !a.onboarding && (a.voice == nil || !a.voice.expanded)
}

func (a *desktop) canBrowseWorkspace(profile tasks.Agent) bool {
	return a.online && a.serverProtocol >= api.WorkspaceFilesProtocol && profile.Workspace != "" && !profile.WorkspaceNeedsReview
}

func (a *desktop) openWorkspaceFile(profile tasks.Agent, relative string) {
	if a.window == nil || a.confirmingFileDiscard || !a.canBrowseWorkspace(profile) {
		return
	}
	chatID := a.currentConversation()
	b := a.files[chatID]
	if b == nil {
		b = &workspaceFilesPane{app: a}
		if a.files == nil {
			a.files = make(map[string]*workspaceFilesPane)
		}
		a.files[chatID] = b
	}
	show := func() {
		if a.currentConversation() == chatID && a.section == "" {
			a.conversationTab = conversationFilesTab
		}
	}
	if b.sameItem(profile, relative) {
		show()
		b.refresh(profile, relative)
		return
	}
	b.afterDiscard(func() {
		show()
		b.load(profile, relative)
	})
}

func (a *desktop) syncWorkspaceFiles() {
	var visible *workspaceFilesPane
	if a.showingWorkspaceFiles() {
		visible = a.currentWorkspaceFiles()
	}
	if a.visibleFiles != visible {
		if a.visibleFiles != nil {
			a.visibleFiles.hide()
		}
		a.visibleFiles = visible
	}
	if visible != nil && visible.refreshOnShow {
		profile, relative := visible.profile, visible.relative
		if current := a.agentProfile(profile.ID); current.ID == profile.ID {
			if current.Workspace != profile.Workspace {
				relative = "."
			}
			profile = current
		}
		visible.refresh(profile, relative)
	}
}

func (b *workspaceFilesPane) sameItem(profile tasks.Agent, relative string) bool {
	return b.profile.ID == profile.ID && b.profile.Workspace == profile.Workspace && b.relative == relative
}

func (b *workspaceFilesPane) hide() {
	b.image = nil
	b.refreshOnShow = true
	if b.cancelLoad != nil {
		b.request++
		b.cancelLoad()
		b.cancelLoad, b.loading = nil, false
	}
}

func (b *workspaceFilesPane) refresh(profile tasks.Agent, relative string) {
	if b.loading || b.saving || b.dirty() || b.app.window == nil || b.app.confirmingFileDiscard || !b.app.canBrowseWorkspace(profile) {
		return
	}
	b.load(profile, relative)
}

func (a *desktop) workspaceTabs(c *ui.Context) {
	b := a.currentWorkspaceFiles()
	if b == nil {
		return
	}
	title, icon := path.Base(b.relative), fileMark
	if b.item.Kind == "folder" || b.relative == "." {
		icon = repoMark
	}
	if b.relative == "." {
		title = "Workspace"
	}
	chat := a.currentConversation()
	closeFile := func() {
		b.afterDiscard(func() {
			delete(a.files, chat)
			if a.currentConversation() == chat {
				a.conversationTab = conversationChatTab
			}
		})
	}
	t := c.Theme()
	chatActive := a.conversationTab == conversationChatTab
	tabs := ui.TabsBase(c.Key("workspace-tabs"), &a.conversationTab, 2)
	tabs.List.Label("Open in this chat").Height(36).FillWidth().Shrink(0).Padding(0, 16).Gap(4).
		AlignItems(ui.Stretch).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		chatTab := tabs.Tab(conversationChatTab).Label("Chat").Padding(0, 9).Gap(6)
		chatColor := workspaceTabColor(c, chatTab, chatActive)
		chatTab.Children(func() {
			ui.Icon(c, buddyMark).Size(14, 14).TextColor(chatColor)
			ui.Text(c, "Chat").FontSize(12).FontWeight(640).TextColor(chatColor)
		})
		ui.Box(c).Size(1, 15).AlignSelf(ui.Center).Shrink(0).Background(t.Border)
		ui.Row(c.Key("file-tab-group")).MinWidth(0).AlignItems(ui.Center).Padding(0, 5, 0, 0).Children(func() {
			fileTab := tabs.Tab(conversationFilesTab).Label(title).Tooltip(b.relative).
				MaxWidth(300).MinWidth(0).Shrink(1).FillHeight().Padding(0, 6, 0, 9).Gap(6)
			fileColor := workspaceTabColor(c, fileTab, !chatActive)
			fileTab.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("Close file tab").Disabled(b.saving || a.confirmingFileDiscard).Chosen() {
					closeFile()
				}
			})
			fileTab.Children(func() {
				ui.Icon(c, icon).Size(14, 14).Shrink(0).TextColor(fileColor)
				ui.Text(c, title).SingleLine().MinWidth(0).FontSize(12).FontWeight(640).TextColor(fileColor)
				if b.dirty() {
					ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(t.Accent).Label("Unsaved changes")
				}
			})
			quietIcon(c.Key("close-file-tab"), "Close "+title, closeMark, b.saving || a.confirmingFileDiscard, closeFile)
		})
	})
}

func workspaceTabColor(c *ui.Context, tab ui.Element, active bool) ui.Color {
	t := c.Theme()
	if active {
		tab.BorderWidth(0, 0, 2, 0).BorderColor(t.Accent)
	}
	if tab.Hovered() {
		tab.Background(t.SurfaceHover)
	}
	if active || tab.Hovered() {
		return t.Text
	}
	return t.TextMuted
}

func (b *workspaceFilesPane) dirty() bool {
	return b.source.Buffer != nil && b.source.Buffer.Modified()
}

func (a *desktop) hasWorkspaceEdits() bool {
	for _, b := range a.files {
		if b.saving || b.dirty() {
			return true
		}
	}
	return false
}

func (a *desktop) afterWorkspaceDiscard(action func()) {
	if a.hasWorkspaceSaves() {
		a.workspaceClose.afterSave = action
		if a.window != nil {
			a.window.Invalidate()
		}
		return
	}
	dirty, filename := 0, ""
	for _, b := range a.files {
		if b.dirty() {
			dirty++
			filename = path.Base(b.relative)
		}
	}
	if dirty == 0 {
		action()
		return
	}
	message := "Discard your changes to " + filename + "?"
	if dirty > 1 {
		message = fmt.Sprintf("Discard your changes to %d workspace files?", dirty)
	}
	a.confirmWorkspaceDiscard(message, func() {
		for id, b := range a.files {
			if b.dirty() {
				delete(a.files, id)
			}
		}
		if a.currentWorkspaceFiles() == nil {
			a.conversationTab = conversationChatTab
		}
		action()
	})
}

func (a *desktop) hasWorkspaceSaves() bool {
	for _, b := range a.files {
		if b.saving {
			return true
		}
	}
	return false
}

func (a *desktop) workspaceSaveFinished(filename string, err error) {
	if a.workspaceClose.afterSave == nil {
		return
	}
	if err != nil {
		a.workspaceClose.afterSave, a.workspaceClose.failed = nil, filename
	} else if !a.hasWorkspaceSaves() {
		action := a.workspaceClose.afterSave
		a.workspaceClose.afterSave = nil
		a.afterWorkspaceDiscard(action)
	}
}

func (a *desktop) workspaceSaveNotice(c *ui.Context) {
	const waitID = "workspace-save-wait"
	state := &a.workspaceClose
	if state.afterSave != nil && !state.waitShown {
		state.waitShown = true
		c.AddToast(ui.Toast{ID: waitID, Title: "Saving workspace files…", Description: "Buddy will continue when the saves finish.",
			Timeout: -1, Action: "Keep open", OnClose: func() { state.afterSave, state.waitShown = nil, false }})
	} else if state.afterSave == nil && state.waitShown {
		c.CloseToast(waitID)
	}
	if state.failed != "" {
		c.AddToast(ui.Toast{Title: "Couldn’t save " + state.failed,
			Description: "Your edits are still in the “" + state.failed + "” tab in its chat.", Type: "error"})
		state.failed = ""
	}
}

func (b *workspaceFilesPane) afterDiscard(action func()) {
	if b.saving || b.app.confirmingFileDiscard {
		return
	}
	if !b.dirty() {
		action()
		return
	}
	b.app.confirmWorkspaceDiscard("Discard your changes to "+path.Base(b.relative)+"?", action)
}

func (a *desktop) confirmWorkspaceDiscard(message string, action func()) {
	if a.window == nil || a.confirmingFileDiscard {
		return
	}
	a.confirmingFileDiscard = true
	win := a.window
	go func() {
		result, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "Unsaved changes", Message: message,
			Detail: "You can cancel and save or copy your edits first.", Buttons: []string{"Cancel", "Discard changes"},
			DestructiveButtons: []int{1},
		})
		win.Update(func() {
			a.confirmingFileDiscard = false
			if err == nil && result.Button == 1 {
				action()
			}
		})
	}()
}

func (b *workspaceFilesPane) load(profile tasks.Agent, relative string) {
	if b.cancelLoad != nil {
		b.cancelLoad()
	}
	same := b.sameItem(profile, relative)
	if !same || b.dirty() {
		b.item, b.source = tasks.WorkspaceItem{}, editor.State{}
	}
	if !same {
		b.preview = renderedFile(relative)
	}
	b.request++
	request := b.request
	b.profile, b.relative, b.loading = profile, relative, true
	b.image, b.refreshOnShow, b.status, b.err = nil, false, "", ""
	ctx, cancel := context.WithCancel(b.app.ctx)
	b.cancelLoad = cancel
	win := b.app.window
	win.Invalidate()
	client := b.app.client
	go func() {
		defer cancel()
		item, err := client.WorkspaceItem(ctx, profile.ID, profile.Workspace, relative)
		var picture *ui.Bitmap
		if err == nil && ctx.Err() == nil && item.Kind == "image" {
			picture = decodeWorkspaceImage(item.Image)
			if picture == nil {
				item.Kind = "unsupported"
			}
			item.Image = nil
		}
		win.Update(func() { b.finishLoad(request, item, picture, err) })
	}()
}

func (b *workspaceFilesPane) finishLoad(request uint64, item tasks.WorkspaceItem, picture *ui.Bitmap, err error) {
	if b.request != request {
		return
	}
	b.cancelLoad, b.loading = nil, false
	if !b.app.showingWorkspaceFiles() || b.app.currentWorkspaceFiles() != b {
		b.refreshOnShow = true
		return
	}
	if err != nil {
		b.err = errorMessage(err, "Couldn't open this workspace item.")
		return
	}
	if item.Kind != "text" {
		b.source = editor.State{}
	} else if b.source.Buffer == nil || b.item.Text != item.Text {
		buffer := editor.NewBuffer(item.Text)
		buffer.SetLanguage(editor.LanguageFor(path.Base(b.relative)))
		b.source = editor.State{Buffer: buffer, SoftWrap: true, Label: "File editor"}
	}
	b.item, b.image = item, picture
}

func decodeWorkspaceImage(data []byte) *ui.Bitmap {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 10000 || cfg.Height > 10000 ||
		int64(cfg.Width)*int64(cfg.Height) > 25_000_000 {
		return nil
	}
	picture, _ := ui.DecodeBitmap(data)
	return picture
}

func (b *workspaceFilesPane) save() {
	if b.loading || b.saving || b.app.confirmingFileDiscard || b.item.ReadOnly || !b.dirty() {
		return
	}
	snapshot, revision := b.source.Buffer.Snapshot(), b.item.Revision
	if snapshot.Len() > tasks.MaxWorkspaceTextBytes {
		b.err = "Save a UTF-8 text file up to 512 KB."
		return
	}
	b.saving, b.err, b.status = true, "", ""
	profile, relative, client, win := b.profile, b.relative, b.app.client, b.app.window
	original := b.item.Text
	go func() {
		text := workspaceEditedText(original, snapshot.String())
		saved, err := client.SaveWorkspaceFile(b.app.ctx, profile.ID, profile.Workspace, relative,
			tasks.WorkspaceFileEdit{Revision: revision, Text: text})
		win.Update(func() {
			b.saving = false
			defer b.app.workspaceSaveFinished(path.Base(relative), err)
			if err != nil {
				b.err = errorMessage(err, "Couldn't save this file. Your edits are still here.")
				return
			}
			b.item.Revision, b.item.Text = saved, text
			if b.source.Buffer.Version() == snapshot.Version() {
				b.source.Buffer.MarkSaved()
			}
			b.status = "Saved"
			b.app.refreshSoon()
		})
	}()
}

func renderedFile(relative string) bool {
	switch strings.ToLower(path.Ext(relative)) {
	case ".md", ".markdown", ".diff", ".patch":
		return true
	}
	return false
}

func (b *workspaceFilesPane) view(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyS) {
		b.save()
	}
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
		b.toolbar(c)
		if b.err != "" {
			ui.Text(c, b.err).Padding(9, 16).TextColor(c.Theme().Danger).Selectable()
		}
		switch {
		case b.loading:
			ui.Text(c, "Opening…").Padding(16).TextColor(c.Theme().TextMuted)
		case b.item.Kind == "folder":
			b.folder(c)
		case b.item.Kind == "text":
			if b.preview {
				ui.Scroll(c.Key("file-preview")).Grow(1).MinHeight(0).FillWidth().Padding(22).Children(func() {
					ui.Column(c).FillWidth().MaxWidth(760).AlignSelf(ui.Center).Children(func() {
						messageMarkdown(c.Key(b.request), b.source.Buffer.String())
					})
				})
			} else {
				b.source.ReadOnly = b.saving || b.item.ReadOnly
				editor.View(c.Key(b.request), &b.source).Fill().MinHeight(0).MinWidth(0)
			}
		case b.item.Kind == "image" && b.image != nil:
			ui.Image(c.Key("file-image"), b.image).Fit(ui.Contain).Fill().MinHeight(0).Label(path.Base(b.relative))
		case b.item.Kind == "image" && b.err == "":
			ui.Text(c, "The preview will reload when the workspace is available.").Padding(16).TextColor(c.Theme().TextMuted)
		case b.item.Kind == "unsupported":
			ui.Text(c, "Preview unavailable for this file type or size.").Padding(16).TextColor(c.Theme().TextMuted)
		}
	})
}

func (b *workspaceFilesPane) toolbar(c *ui.Context) {
	busy := b.loading || b.saving || b.app.confirmingFileDiscard
	t := c.Theme()
	ui.Row(c).FillWidth().MinWidth(0).MinHeight(40).Padding(4, 16).Gap(8).AlignItems(ui.Center).
		Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		if b.relative != "." && b.relative != "" {
			quietIcon(c.Key("file-up"), "Go to parent folder", backMark, busy,
				func() { b.afterDiscard(func() { b.load(b.profile, path.Dir(b.relative)) }) })
		}
		parent := b.relative
		if b.item.Kind != "folder" {
			parent = path.Dir(parent)
		}
		breadcrumb := "Workspace"
		if b.app.chat.Name != "" {
			breadcrumb = b.profile.Name + "’s workspace"
		}
		if parent != "." && parent != "" {
			breadcrumb += " / " + parent
		}
		ui.Text(c.Key("file-path"), breadcrumb).SingleLine().Tooltip(b.relative).
			FontSize(12).TextColor(t.TextMuted).Grow(1).MinWidth(0)
		if b.item.Kind == "text" {
			b.fileEditingControls(c, busy)
		}
		if b.status != "" && !busy && !b.dirty() && b.err == "" {
			ui.Text(c, b.status).FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		btn := ui.ButtonBase(c.Key("file-actions")).Size(26, 26).Shrink(0).Radius(6).Label("File actions").
			Tooltip("File actions").Disabled(busy).PopoverMenuButton(func(menu *ui.Menu) {
			if menu.Item("Reload").Chosen() {
				b.afterDiscard(func() { b.load(b.profile, b.relative) })
			}
			if !b.app.remoteHost && b.profile.Workspace != "" && menu.Item("Show in Finder").Chosen() {
				if b.relative == "." {
					_ = mygo.Shell.OpenPath(b.profile.Workspace)
				} else {
					mygo.Shell.ShowItemInFolder(filepath.Join(b.profile.Workspace, filepath.FromSlash(b.relative)))
				}
			}
		})
		if btn.Hovered() {
			btn.Background(t.SurfacePressed)
		}
		btn.Children(func() { ui.Icon(c, moreMark).Size(15, 15).TextColor(t.TextMuted) })
	})
}

func (b *workspaceFilesPane) fileEditingControls(c *ui.Context, busy bool) {
	if renderedFile(b.relative) {
		ui.Row(c).Shrink(0).Gap(2).Padding(2).Radius(7).Background(c.Theme().SurfaceHover).
			Border(1, c.Theme().Border).Children(func() {
			label := "Edit"
			if b.item.ReadOnly {
				label = "Source"
			}
			fileMode(c.Key("file-preview-mode"), "Preview", b.preview, func() { b.preview = true })
			fileMode(c.Key("file-edit-mode"), label, !b.preview, func() { b.preview = false })
		})
	}
	if b.item.ReadOnly {
		ui.Text(c, "Read-only").FontSize(11).TextColor(c.Theme().TextMuted)
	} else if !b.preview || b.dirty() || b.saving {
		label := "Save"
		if b.saving {
			label = "Saving…"
		}
		ui.Button(c.Key("file-save"), label).Height(28).Shrink(0).Disabled(busy || !b.dirty()).OnClick(b.save)
	}
}

func fileMode(c *ui.Context, title string, selected bool, action func()) {
	t := c.Theme()
	color := t.TextMuted
	btn := ui.ButtonBase(c).Label(title).Selected(selected).Height(24).Padding(0, 8).Radius(5).OnClick(action)
	if selected || btn.Hovered() {
		color = t.Text
		btn.Background(t.SurfacePressed)
	}
	btn.Children(func() { ui.Text(c, title).FontSize(11).FontWeight(620).TextColor(color) })
}

func (b *workspaceFilesPane) folder(c *ui.Context) {
	ui.Scroll(c.Key(b.request)).Grow(1).MinHeight(0).FillWidth().Padding(10, 16).Gap(2).Children(func() {
		if len(b.item.Entries) == 0 {
			ui.Text(c, "This folder is empty.").Padding(10, 0).TextColor(c.Theme().TextMuted)
		}
		for _, entry := range b.item.Entries {
			icon := fileMark
			if entry.Folder {
				icon = repoMark
			}
			btn := ui.ButtonBase(c.Key("workspace-entry-"+entry.Name)).Label(entry.Name).
				Height(34).FillWidth().Padding(0, 9).Gap(9).Radius(6).Justify(ui.Start).
				OnClick(func() { b.load(b.profile, path.Join(b.relative, entry.Name)) })
			if btn.Hovered() {
				btn.Background(c.Theme().SurfaceHover)
			}
			btn.Children(func() {
				ui.Icon(c, icon).Size(16, 16).TextColor(c.Theme().TextMuted)
				ui.Text(c, entry.Name).SingleLine().Grow(1).MinWidth(0)
				if entry.Folder {
					ui.Icon(c, arrowMark).Size(12, 12).TextColor(c.Theme().TextMuted)
				}
			})
		}
		if b.item.Truncated {
			ui.Text(c, "Showing files from the first 512 directory entries.").Padding(8, 0).TextColor(c.Theme().TextMuted)
		}
	})
}
