package main

import (
	"context"
	"strings"
	"unicode/utf8"

	"buddy/internal/tasks"
)

func (a *desktop) openWorkArtifact(job, id string) {
	if a.window == nil || a.client == nil {
		return
	}
	chat := a.currentConversation()
	pane := a.files[chat]
	if pane == nil {
		pane = &workspaceFilesPane{app: a}
		if a.files == nil {
			a.files = map[string]*workspaceFilesPane{}
		}
		a.files[chat] = pane
	}
	pane.afterDiscard(func() {
		pane.profile = a.focusedAgent()
		a.showWorkspaceFile(chat)
		pane.loadArtifact(job, id)
	})
}

func (b *workspaceFilesPane) loadArtifact(job, id string) {
	if b.cancelLoad != nil {
		b.cancelLoad()
	}
	b.request++
	request := b.request
	b.artifactID, b.artifactJob, b.originTask = id, job, ""
	b.loading, b.err, b.refreshOnShow = true, "", false
	ctx, cancel := context.WithCancel(b.app.ctx)
	b.cancelLoad = cancel
	client, win := b.app.client, b.app.window
	go func() {
		defer cancel()
		artifact, err := client.WorkArtifact(ctx, job, id)
		item := tasks.WorkspaceItem{Kind: "unsupported", ReadOnly: true}
		picture := decodeWorkspaceImage(artifact.Data)
		if picture != nil {
			item.Kind = "image"
		} else if utf8.Valid(artifact.Data) && !strings.ContainsRune(string(artifact.Data), '\x00') {
			item.Kind, item.Text = "text", string(artifact.Data)
		}
		win.Update(func() {
			if b.app.client != client || b.request != request {
				return
			}
			b.relative, b.preview = artifact.Name, true
			b.finishLoad(request, item, picture, err)
		})
	}()
}
