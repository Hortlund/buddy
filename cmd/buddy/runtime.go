package main

import (
	"strings"
	"time"

	"buddy/internal/api"
)

func (a *desktop) loadModels() {
	if !a.online || a.modelsLoading || a.modelsLoaded || a.window == nil {
		return
	}
	a.modelsLoading = true
	win, revision := a.window, a.modelsRevision
	go func() {
		models, err := a.client.CodexModels(a.ctx)
		win.Update(func() {
			a.modelsLoading = false
			if revision != a.modelsRevision {
				if a.section == "agent" {
					a.loadModels()
				}
				return
			}
			if err == nil {
				a.models, a.modelsLoaded = models, true
			}
		})
	}()
}

func (a *desktop) showConnections() {
	if a.saving {
		return
	}
	a.section = "connections"
	a.integrations.next = time.Time{}
	a.refreshSoon()
	a.changeAuth("read")
	if a.serverProtocol >= api.VoiceProtocol {
		a.changeVoiceKey("GET")
	}
}

func (a *desktop) changeAuth(action string) {
	if !a.online || a.authBusy || a.window == nil {
		return
	}
	key := strings.TrimSpace(a.authDraft)
	if action == "save" && key == "" {
		return
	}
	a.authBusy, a.authErr = true, ""
	if action == "save" {
		a.authDraft = ""
	}
	win := a.window
	go func() {
		var status api.CodexAuth
		var err error
		switch action {
		case "save":
			status, err = a.client.SaveCodexKey(a.ctx, key)
		case "remove":
			status, err = a.client.RemoveCodexKey(a.ctx)
		default:
			status, err = a.client.CodexAuth(a.ctx)
		}
		win.Update(func() {
			a.authBusy = false
			if err != nil {
				a.authErr = errorMessage(err, "Couldn't update the Codex connection. Try again.")
				return
			}
			a.auth = status
			if action != "read" {
				a.modelsRevision++
				a.models, a.modelsLoaded = nil, false
				if a.section == "agent" {
					a.loadModels()
				}
			}
		})
	}()
}
