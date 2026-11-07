package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
)

var diffBlocks = map[string]func(*ui.Context, *markdown.CodeBlockView){"diff": renderPatch, "patch": renderPatch}

func messageMarkdown(c *ui.Context, text string) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "diff --git ") ||
		strings.HasPrefix(trimmed, "--- ") && strings.Contains(trimmed, "\n+++ ") && strings.Contains(trimmed, "\n@@ ") {
		// The fence is longer than any fence in the message, including one
		// that appears as a literal line in the source under review.
		fence := "```"
		for strings.Contains(trimmed, fence) {
			fence += "`"
		}
		patch := strings.TrimRight(strings.TrimLeft(text, "\r\n\t "), "\r\n")
		text = fence + "diff\n" + patch + "\n" + fence
	}
	link := ui.Hex("#0969DA")
	if c.Theme().Dark {
		link = ui.Hex("#58A6FF")
	}
	markdown.View(c, text, &markdown.Options{Theme: &markdown.Theme{Link: link}, Code: diffBlocks}).
		MinWidth(0).FontSize(14).LineHeight(1.45)
}

type patchDisplay struct {
	source string
	files  []editor.FilePatch
	views  []editor.DiffState
}

func renderPatch(c *ui.Context, block *markdown.CodeBlockView) {
	box := ui.Column(c).FillWidth().MinWidth(250).Radius(9).Border(1, c.Theme().Border).Clip()
	cache := ui.Local(box, "patch", func() patchDisplay { return patchDisplay{} })
	source := block.Code()
	if cache.source != source {
		*cache = patchDisplay{source: source}
		if files, err := editor.ParsePatch(source); err == nil {
			cache.files = files
			cache.views = make([]editor.DiffState, len(files))
			for i, file := range files {
				cache.views[i].Layout = editor.DiffUnified
				cache.views[i].Indicators = editor.DiffClassic
				cache.views[i].SetPatch(file)
			}
		}
	}
	if len(cache.files) == 0 {
		// Bare +/- snippets and incomplete diffs remain readable code.
		box.Children(func() { markdown.DefaultCodeBlock(c, block) })
		return
	}
	box.Children(func() {
		ui.Row(c).Padding(5, 9).Background(c.Theme().Surface).Children(func() {
			ui.Text(c, "Git diff").FontSize(12).FontWeight(650).Grow(1)
			block.CopyButton(c)
		})
		for i, file := range cache.files {
			name := file.NewName
			if file.OldName != "" && name != "" && file.OldName != name {
				name = file.OldName + " → " + name
			} else if name == "" {
				name = file.OldName
			}
			ui.Column(c.Key(i)).FillWidth().MinWidth(0).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
				ui.Text(c, name).Role(ui.RoleHeading).SingleLine().Tooltip(name).Padding(7, 9).
					FontSize(12).FontWeight(620).Background(c.Theme().SurfaceHover)
				if len(file.Hunks) == 0 {
					text := "File metadata changed"
					if file.Binary {
						text = "Binary file changed"
					}
					ui.Text(c, text).Padding(7, 9).TextColor(c.Theme().TextMuted)
					return
				}
				lines := 0
				for _, hunk := range file.Hunks {
					lines += 1 + len(hunk.Lines)
				}
				view := &cache.views[i]
				view.FitContent = lines <= 16
				display := editor.DiffView(c, view).FillWidth().MinWidth(0)
				if !view.FitContent {
					display.Height(270)
				}
			})
		}
	})
}
