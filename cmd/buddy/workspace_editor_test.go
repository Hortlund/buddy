package main

import (
	"testing"

	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestWorkspaceEditorReviewsTheSameUnsavedBuffer(t *testing.T) {
	buffer := editor.NewBuffer("original\n")
	b := &workspaceFilesPane{app: &desktop{}, relative: "main.go",
		item:   tasks.WorkspaceItem{Kind: "text", Text: "original\n", Revision: "unchanged"},
		source: editor.State{Buffer: buffer, SoftWrap: true}}
	view := ui.NewTester(b.view, 1000, 650)
	buffer.Set("changed\n")
	view.Frame()
	if err := view.Click("Changes"); err != nil {
		t.Fatal(err)
	}
	if !b.changes || b.diff.New != buffer || b.diff.Old.String() != "original\n" {
		t.Fatal("review does not compare the saved file with the live editor buffer")
	}
	if err := view.Click("Side by side"); err != nil {
		t.Fatal(err)
	}
	if b.diff.Layout != editor.DiffSplit || !b.diff.Editable {
		t.Fatal("split review is not editable")
	}
	if control, ok := view.AccessByRole(ui.RoleToggleButton, "Side by side"); !ok || !control.Checked {
		t.Fatal("review mode is not exposed to assistive technology")
	}
	b.diff.New.Set("manual fix\n")
	view.Frame()
	saveUIProof(t, "editor.png", view)
	if err := view.Click("Edit"); err != nil {
		t.Fatal(err)
	}
	if !b.dirty() || b.source.Buffer.String() != "manual fix\n" || b.item.Revision != "unchanged" {
		t.Fatal("switching review modes saved or discarded the manual edit")
	}
	if err := view.Click("Find"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("Replace") || !view.HasText("Go to line") {
		t.Fatal("editor tools are missing")
	}
	if _, ok := view.AccessByRole(ui.RoleTextField, "Find"); !ok {
		t.Fatal("Find did not open the native search field")
	}
}

func TestWorkspaceEditorDropsOldBaselineOnReload(t *testing.T) {
	b := &workspaceFilesPane{app: &desktop{}, relative: "file.txt",
		item: tasks.WorkspaceItem{Kind: "text", Text: "old"}, source: editor.State{Buffer: editor.NewBuffer("old")}}
	b.prepareEditor()
	old := b.diff.Old
	b.item = tasks.WorkspaceItem{Kind: "text", Text: "new"}
	b.source = editor.State{Buffer: editor.NewBuffer("new")}
	b.prepareEditor()
	if b.diff.Old == old || b.diff.Old.String() != "new" {
		t.Fatal("new file kept the previous file's diff baseline")
	}
	b.item = tasks.WorkspaceItem{Kind: "folder"}
	b.source = editor.State{}
	b.prepareEditor()
	if b.diff.Old != nil || b.diff.New != nil || b.changes {
		t.Fatal("folder retains the previous editor's buffers")
	}
}

func TestWorkspaceExportUsesOnlyAVisibleEditorSelection(t *testing.T) {
	b := &workspaceFilesPane{source: editor.State{Buffer: editor.NewBuffer("one two")}}
	b.source.Select(0, 3)
	if b.exportText() != "one" {
		t.Fatal("visible source selection was not exported")
	}
	b.preview = true
	if b.exportText() != "one two" {
		t.Fatal("preview exported a hidden editor selection")
	}
	b.preview, b.changes = false, true
	if b.exportText() != "one two" {
		t.Fatal("diff review exported a hidden editor selection")
	}
}
