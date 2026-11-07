package main

import (
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
		if pane.cancelLoad != nil {
			pane.cancelLoad()
		}
		pane.request++
		request := pane.request
		pane.artifactID, pane.artifactJob, pane.originTask = id, job, ""
		pane.loading, pane.err = true, ""
		pane.profile = a.focusedAgent()
		a.showWorkspaceFile(chat)
		client, win := a.client, a.window
		go func() {
			artifact, err := client.WorkArtifact(a.ctx, job, id)
			item := tasks.WorkspaceItem{Kind: "unsupported", ReadOnly: true}
			picture := decodeWorkspaceImage(artifact.Data)
			if picture != nil {
				item.Kind = "image"
			} else if utf8.Valid(artifact.Data) && !strings.ContainsRune(string(artifact.Data), '\x00') {
				item.Kind, item.Text = "text", string(artifact.Data)
			}
			win.Update(func() {
				if a.client != client || pane.request != request {
					return
				}
				pane.relative, pane.preview = artifact.Name, true
				pane.finishLoad(request, item, picture, err)
			})
		}()
	})
}
