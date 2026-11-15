package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func (b *workspaceFilesPane) exportText() string {
	if b.source.Buffer == nil {
		return ""
	}
	if !b.preview && !b.changes {
		if selected := b.source.SelectedText(); selected != "" {
			return selected
		}
	}
	return b.source.Buffer.String()
}

func (b *workspaceFilesPane) prepareEditor() {
	if b.item.Kind != "text" || b.source.Buffer == nil {
		b.diff, b.changes = editor.DiffState{}, false
		return
	}
	if b.item.ReadOnly {
		b.changes = false
	}
	if b.source.DiffBase == nil {
		b.source.DiffBase = editor.NewBuffer(b.item.Text)
		b.source.DiffBase.SetLanguage(b.source.Buffer.Language())
	}
	if b.diff.Old != b.source.DiffBase || b.diff.New != b.source.Buffer {
		b.diff = editor.DiffState{Old: b.source.DiffBase, New: b.source.Buffer,
			Layout: editor.DiffUnified, Indicators: editor.DiffClassic, Wrap: true}
	}
	b.source.ReadOnly = b.saving || b.item.ReadOnly
	b.diff.Editable = !b.source.ReadOnly
}

func (b *workspaceFilesPane) editorCommand(command editor.Command) {
	if b.changes {
		b.diff.Run(command)
	} else {
		b.source.Run(command)
	}
}

func (b *workspaceFilesPane) editorToolbar(c *ui.Context) {
	ui.Row(c.Key("editor-tools")).FillWidth().MinWidth(0).Padding(4, 16).Gap(6).Wrap().
		AlignItems(ui.Center).Background(c.Theme().Surface).Children(func() {
		ui.Button(c.Key("editor-find"), "Find").Height(26).Tooltip("Find in file (⌘F)").
			OnClick(func() { b.editorCommand(editor.Find) })
		ui.Button(c.Key("editor-replace"), "Replace").Height(26).Disabled(b.source.ReadOnly).
			OnClick(func() { b.editorCommand(editor.FindReplace) })
		ui.Button(c.Key("editor-go-line"), "Go to line").Height(26).
			OnClick(func() { b.editorCommand(editor.GoToLine) })
		if b.changes {
			ui.Text(c, "Against saved file").FontSize(11).TextColor(c.Theme().TextMuted)
			fileMode(c.Key("changes-unified"), "Unified", b.diff.Layout == editor.DiffUnified,
				func() { b.diff.Layout = editor.DiffUnified })
			fileMode(c.Key("changes-split"), "Side by side", b.diff.Layout == editor.DiffSplit,
				func() { b.diff.Layout = editor.DiffSplit })
			ui.Button(c.Key("changes-next"), "Next change").Height(26).Disabled(len(b.diff.Hunks()) == 0).
				OnClick(func() { nextDiffChange(&b.diff) })
		} else {
			ui.Checkbox(c.Key("editor-wrap"), &b.source.SoftWrap, "Wrap lines")
		}
		position := b.source.CursorPos()
		if b.changes {
			position = b.diff.CursorPos()
		}
		ui.Text(c, fmt.Sprintf("Ln %d, Col %d", position.Line+1, position.Col+1)).FontSize(11).
			TextColor(c.Theme().TextMuted).Grow(1).AlignSelf(ui.Center)
	})
}
