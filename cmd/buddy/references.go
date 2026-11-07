package main

import (
	"net/url"
	"path"
	"strings"

	"buddy/internal/references"
	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func (a *desktop) referenceMarkdown(c *ui.Context, text, bud string, base ...string) {
	services := c.Services()
	messageMarkdown(c, text, func(raw string) {
		if len(base) != 0 {
			if u, err := url.Parse(raw); err == nil && u.Scheme == "" && u.Host == "" && u.Path != "" {
				u.Path, u.RawPath = path.Join(base[0], u.Path), ""
				raw = u.String()
			}
		}
		a.openReference(raw, bud, services.OpenURL)
	})
}

func (a *desktop) openReference(raw, bud string, openWeb func(string)) {
	if target, ok := references.Parse(raw); ok {
		switch target.Kind {
		case "work":
			a.openWork(tasks.WorkItem{ID: target.Job, ConversationID: target.ID})
		case "chat":
			a.openConversation(target.ID)
		case "files":
			a.openReferencedFile(target.ID, target.Path)
		}
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return
	}
	if (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		openWeb(raw)
		return
	}
	if u.Scheme == "" && u.Host == "" && u.RawQuery == "" && u.Path != "" {
		relative := strings.TrimPrefix(u.Path, "./")
		if references.ValidPath(relative) {
			a.openReferencedFile(bud, path.Clean(relative))
		}
	}
}

func (a *desktop) openReferencedFile(bud, relative string) {
	if bud == "" {
		bud = a.focusedAgent().ID
	}
	a.referenceRequest++
	request := a.referenceRequest
	profile := a.agentProfile(bud)
	if profile.ID == bud && profile.Workspace != "" {
		a.openWorkspaceFile(profile, relative)
		return
	}
	// Work results can refer to a teammate who isn't in the selected chat.
	client, win, chat, ctx := a.client, a.window, a.currentConversation(), a.ctx
	if client == nil || a.window == nil {
		return
	}
	go func() {
		profile, err := client.Agent(ctx, bud)
		win.Update(func() {
			if a.client != client || a.currentConversation() != chat || a.referenceRequest != request {
				return
			}
			if err != nil {
				a.chatErr = errorMessage(err, "Couldn't open this Bud’s file.")
				return
			}
			a.openWorkspaceFile(profile, relative)
		})
	}()
}
