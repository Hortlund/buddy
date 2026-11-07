package main

import (
	"testing"

	"buddy/internal/references"
	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func TestMessageLinksNavigateAndRejectExecutableURLs(t *testing.T) {
	a := &desktop{}
	var opened []string
	openWeb := func(raw string) { opened = append(opened, raw) }
	for _, raw := range []string{"https://example.com/docs", "javascript:alert(1)", "file:///etc/passwd", "https://user:secret@example.com"} {
		a.openReference(raw, "agt_orbit", openWeb)
	}
	if len(opened) != 1 || opened[0] != "https://example.com/docs" {
		t.Fatalf("external URL handling: %v", opened)
	}
	a.openReference(references.Work("conv_team", "tsk_build"), "agt_orbit", openWeb)
	if a.section != "work" || a.work.selected.ID != "tsk_build" || a.work.selected.ConversationID != "conv_team" {
		t.Fatal("job reference did not navigate to the actual assignment")
	}
	var clicked string
	view := ui.NewTester(func(c *ui.Context) {
		messageMarkdown(c, "See [the plan](buddy://files/agt_orbit?path=plan.md).", func(raw string) { clicked = raw })
	}, 500, 220)
	if err := view.Click("the plan"); err != nil {
		t.Fatal(err)
	}
	if clicked != "buddy://files/agt_orbit?path=plan.md" {
		t.Fatalf("native Markdown link wasn't clickable: %q", clicked)
	}
}

func TestFileReferenceNavigationLeavesWorkAndKeepsTheCallAlive(t *testing.T) {
	a := &desktop{section: "work", conversationID: "conv_team", voice: &desktopVoice{expanded: true}}
	a.chat = tasks.Conversation{ID: "conv_team", Agent: tasks.Agent{ID: "agt_orbit", Name: "Orbit"}}
	a.files = map[string]*workspaceFilesPane{"conv_team": {app: a, relative: "plan.md", item: tasks.WorkspaceItem{Kind: "unsupported"}}}
	a.showWorkspaceFile("conv_team")
	view := ui.NewTester(a.view, 1120, 750)
	if !a.showingWorkspaceFiles() || a.voice == nil || a.voice.expanded || a.section != "" || !view.HasText("plan.md") {
		t.Fatalf("Work/call file reference didn't reveal its tab: %v", view.Texts())
	}
	work := workBrowser{selected: tasks.WorkItem{ID: "tsk_job", ConversationID: "old-chat"}}
	work.finishDetailRead(0, 0, 0, tasks.WorkDetails{WorkItem: tasks.WorkItem{ID: "tsk_job", ConversationID: "new-group"}}, nil)
	if work.selected.ConversationID != "new-group" {
		t.Fatal("resolved job details retained the stale chat for actions")
	}
}
