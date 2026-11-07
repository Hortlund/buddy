package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var (
	attachMark  = svg(`<path d="m8 13 6.5-6.5a2.5 2.5 0 0 1 3.5 3.5l-8 8a4 4 0 0 1-5.7-5.7l8-8"/>`)
	micMark     = svg(`<rect x="9" y="3" width="6" height="12" rx="3"/><path d="M6 11v1a6 6 0 0 0 12 0v-1m-6 7v3m-3 0h6"/>`)
	pictureMark = svg(`<rect x="3" y="4" width="18" height="16" rx="3"/><path d="m4 16 5-5 4 4 3-3 4 4"/><circle cx="15.5" cy="8.5" r=".7"/>`)
	closeMark   = svg(`<path d="m7 7 10 10m0-10L7 17"/>`)
)

func (a *desktop) chooseAttachments() {
	if a.window == nil {
		return
	}
	win := a.window
	a.collectAttachments(func() ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: "Attach to chat", ButtonLabel: "Attach", Multiple: true,
		})
	})
}

func (a *desktop) collectAttachments(pick func() ([]string, error)) {
	if a.window == nil || a.pickingFiles || a.sending {
		return
	}
	win, conversation := a.window, a.currentConversation()
	existing := slices.Clone(a.attachments)
	a.pickingFiles = true
	go func() {
		paths, err := pick()
		var files []tasks.Attachment
		if err == nil && len(paths) != 0 {
			files, err = loadAttachments(paths, existing)
		}
		win.Update(func() {
			a.pickingFiles = false
			if a.currentConversation() != conversation {
				return
			}
			if err != nil {
				a.chatErr = err.Error()
				return
			}
			if len(files) != 0 {
				a.setAttachments(files)
				a.composerInput.Focus()
			}
		})
	}()
}

func loadAttachments(paths []string, existing []tasks.Attachment) ([]tasks.Attachment, error) {
	if len(paths)+len(existing) > tasks.MaxAttachments {
		return nil, errors.New("Attach up to five files per message.")
	}
	remaining := tasks.MaxAttachmentBytes
	for _, f := range existing {
		remaining -= f.Size
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Couldn't open %s as a file.", filepath.Base(path))
		}
		if info.Size() > int64(remaining) {
			return nil, errors.New("Attachments can total up to 8 MB per message.")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(file, int64(remaining)+1))
		file.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > remaining {
			return nil, errors.New("Attachments can total up to 8 MB per message.")
		}
		existing = append(existing, tasks.Attachment{Name: filepath.Base(path), Data: data})
		remaining -= len(data)
	}
	return tasks.PrepareAttachments(existing)
}

func (a *desktop) pasteAttachment() bool {
	if paths, err := mygo.Clipboard.ReadFiles(); err == nil && len(paths) != 0 {
		if !a.canSendAttachments() {
			a.chatErr = "Update the Buddy server to attach files."
			return true
		}
		a.collectAttachments(func() ([]string, error) { return paths, nil })
		return true
	}
	data := mygo.Clipboard.ReadImage()
	if len(data) == 0 {
		return false
	}
	if !a.canSendAttachments() {
		a.chatErr = "Update the Buddy server to attach images."
		return true
	}
	if a.sending || a.pickingFiles {
		return true
	}
	name := "Pasted image.png"
	for i := 2; slices.ContainsFunc(a.attachments, func(f tasks.Attachment) bool { return f.Name == name }); i++ {
		name = fmt.Sprintf("Pasted image %d.png", i)
	}
	files, err := tasks.PrepareAttachments(append(slices.Clone(a.attachments), tasks.Attachment{Name: name, Data: data}))
	if err != nil {
		a.chatErr = err.Error()
	} else {
		a.setAttachments(files)
	}
	return true
}

func (a *desktop) setAttachments(files []tasks.Attachment) {
	a.attachments, a.chatErr, a.retryKey = files, "", ""
}

func attachmentIcon(file tasks.Attachment) *ui.SVG {
	if strings.HasPrefix(file.Type, "image/") {
		return pictureMark
	}
	return attachMark
}

func quietIcon(c *ui.Context, label string, icon *ui.SVG, disabled bool, action func()) {
	button := ui.ButtonBase(c).Size(30, 30).Shrink(0).Radius(8).Center().AlignSelf(ui.Center).
		Label(label).Tooltip(label).Disabled(disabled).OnClick(action)
	if button.Hovered() && !disabled {
		button.Background(c.Theme().SurfaceHover)
	}
	button.Children(func() { ui.Icon(c, icon).Size(17, 17).TextColor(c.Theme().TextMuted) })
}

func (a *desktop) attachmentDrafts(c *ui.Context) {
	if len(a.attachments) == 0 {
		return
	}
	ui.Row(c.Key("attachment-drafts")).Wrap().FillWidth().Gap(5).Children(func() {
		for i, file := range a.attachments {
			ui.Row(c.Key(i)).AlignSelf(ui.Start).MaxWidth(245).Gap(6).Height(29).
				Padding(0, 6).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
				ui.Icon(c, attachmentIcon(file)).Size(14, 14).TextColor(c.Theme().TextMuted)
				ui.Text(c, file.Name).FontSize(12).SingleLine().MinWidth(0)
				quietIcon(c.Key("remove"), "Remove "+file.Name, closeMark, a.sending || a.pickingFiles, func() {
					a.setAttachments(slices.Delete(slices.Clone(a.attachments), i, i+1))
				})
			})
		}
	})
}

func (a *desktop) saveAttachment(task string, position int, name string) {
	client := a.client
	a.downloadAttachment(name, func() (tasks.Attachment, error) { return client.Attachment(a.ctx, task, position) })
}

func (a *desktop) downloadAttachment(name string, load func() (tasks.Attachment, error)) {
	if a.window == nil || !a.online || a.savingFile {
		return
	}
	win, id := a.window, a.currentConversation()
	a.savingFile = true
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: "Save attachment", DefaultPath: defaultDownload(name)})
		if err == nil && path != "" {
			var file tasks.Attachment
			file, err = load()
			if err == nil {
				// The save panel handles overwrite confirmation. Replace by rename
				// so a failed transfer never truncates the existing destination.
				err = saveDownloadedFile(path, file.Data)
			}
		}
		win.Update(func() {
			a.savingFile = false
			if err != nil && a.currentConversation() == id {
				a.chatErr = errorMessage(err, "Couldn't save the attachment.")
			}
		})
	}()
}

func (a *desktop) generatedImageView(c *ui.Context, task string) {
	type preview struct {
		picture *ui.Bitmap
		loading bool
		failed  bool
	}
	panel := ui.Column(c).FillWidth().MaxWidth(360).Gap(5).Margin(4, 0, 0)
	state := ui.Local(panel, "image-preview", func() preview { return preview{} })
	client, win, ctx := a.client, a.window, a.ctx
	if state.picture == nil && !state.loading && !state.failed && a.online && win != nil {
		state.loading = true
		go func() {
			file, err := client.GeneratedImage(ctx, task)
			var picture *ui.Bitmap
			if err == nil {
				picture = decodeWorkspaceImage(file.Data)
			}
			win.Update(func() {
				state.picture, state.loading, state.failed = picture, false, picture == nil
			})
		}()
	}
	panel.Children(func() {
		switch {
		case state.picture != nil:
			ui.Image(c.Key("preview"), state.picture).Label("Generated image").
				Fit(ui.Contain).FillWidth().Height(265).Radius(8).Background(c.Theme().Surface)
		case state.loading:
			ui.Text(c, "Loading image…").TextColor(c.Theme().TextMuted)
		case state.failed:
			ui.Button(c.Key("retry"), "Retry image preview").AlignSelf(ui.Start).
				Disabled(!a.online).OnClick(func() { state.failed = false })
		}
		ui.Button(c.Key("download"), "Save image…").AlignSelf(ui.Start).Disabled(!a.online || a.savingFile).
			OnClick(func() {
				a.downloadAttachment(tasks.GeneratedImageName, func() (tasks.Attachment, error) {
					return client.GeneratedImage(ctx, task)
				})
			})
	})
}

func defaultDownload(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return name
	}
	downloads := filepath.Join(home, "Downloads")
	if info, err := os.Stat(downloads); err == nil && info.IsDir() {
		return filepath.Join(downloads, name)
	}
	return filepath.Join(home, name)
}

func saveDownloadedFile(path string, data []byte) error {
	out, err := os.CreateTemp(filepath.Dir(path), ".buddy-download-")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	_, writeErr := out.Write(data)
	if err := errors.Join(writeErr, out.Close()); err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}
