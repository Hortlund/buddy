package main

import (
	"testing"

	"buddy/internal/references"
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
