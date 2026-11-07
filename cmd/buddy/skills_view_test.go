package main

import (
	"testing"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func TestComposerSkillPicker(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.SkillSelectionProtocol, draft: "/tri"}
	a.directory.Skills = []tasks.SkillInfo{{Name: "plan", Description: "Plan work."}, {Name: "triage-ci", Description: "Triage a failure."}}
	view := ui.NewTester(a.composer, 760, 340)
	if !view.HasText("/triage-ci") || view.HasText("/plan") {
		t.Fatalf("slash menu did not filter: %v", view.Texts())
	}
	bounds, ok := view.Find("/triage-ci")
	if !ok {
		t.Fatal("suggested skill has no accessible button")
	}
	x, y := bounds.X+bounds.W/2, bounds.Y+bounds.H/2
	view.Press(x, y)
	view.Release(x, y)
	if a.draft != "/triage-ci " || view.HasText("Triage a failure.") {
		t.Fatalf("selecting a skill should leave its arguments editable: %q", a.draft)
	}
	view.Type("check the run")
	if a.draft != "/triage-ci check the run" {
		t.Fatalf("typing after clicking a skill inserted text at the wrong position: %q", a.draft)
	}
	a.draft = "/pl"
	view.Frame()
	if err := view.Click("Message"); err != nil {
		t.Fatal(err)
	}
	view.Key(0, ui.KeyTab)
	if a.draft != "/plan " {
		t.Fatalf("Tab did not complete the first matching skill: %q", a.draft)
	}
	view.Type("review auth")
	if a.draft != "/plan review auth" {
		t.Fatalf("typing after Tab inserted text at the wrong position: %q", a.draft)
	}
	a.draft = "/tmp/build.log"
	view.Frame()
	if view.HasText("Skills · Tab to complete") {
		t.Fatal("an absolute file path opened the slash menu")
	}
}

func TestSelectedSkillRemainsAQueuedFollowUpAfterRemoval(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.SkillSelectionProtocol, draft: "/tri"}
	a.directory.Skills = []tasks.SkillInfo{{Name: "triage-ci"}}
	a.chat.Active = &tasks.ActiveRun{ID: "running", Status: tasks.Running, CanSteer: true}
	view := ui.NewTester(a.composer, 760, 340)
	if err := view.Click("/triage-ci"); err != nil {
		t.Fatal(err)
	}
	view.Type("investigate the test")
	a.directory.Skills = nil
	view.Frame()
	if a.skillForDraft(a.draft) != "triage-ci" {
		t.Fatalf("catalog refresh discarded the selected skill: %q", a.draft)
	}
	if _, ok := view.Find("Queue"); !ok || view.HasText("Steer now") {
		t.Fatalf("a selected skill should only be offered as a follow-up: %v", view.Texts())
	}
	a.draft = "/tmp/logs.txt"
	view.Frame()
	if _, ok := view.Find("Steer now"); !ok {
		t.Fatalf("ordinary text should still be steerable: %v", view.Texts())
	}
}

func TestBudProposalCardShowsProfileAndCreationState(t *testing.T) {
	a := &desktop{online: true}
	message := tasks.Message{TaskID: "turn", BudProposal: &tasks.BudProposal{
		BudDraft: tasks.BudDraft{Name: "Finch", Role: "CI specialist", Instructions: "Investigate failed builds."},
	}}
	view := ui.NewTester(func(c *ui.Context) { a.budProposalCard(c, message) }, 420, 240)
	if !view.HasText("Bud draft") || !view.HasText("Investigate failed builds.") {
		t.Fatalf("Bud proposal wasn't reviewable: %v", view.Texts())
	}
	if _, ok := view.Find("Create Bud"); !ok {
		t.Fatal("draft was missing its create action")
	}
	message.BudProposal.ConversationID = "conv_finch"
	view.Frame()
	if _, ok := view.Find("Open chat"); !ok || view.HasText("Bud draft") {
		t.Fatalf("created card did not link to the chat: %v", view.Texts())
	}
}
