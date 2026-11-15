package main

import (
	"context"
	"fmt"

	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type patchReview struct {
	window   *mygo.Window
	media    mediaExport
	dark     bool
	raw      editor.State
	result   tasks.GitHubResult
	patch    patchDisplay
	selected int
}

// MyGo's main thread owns this set, including close callbacks.
var patchReviewWindows = make(map[*mygo.Window]struct{})

func openPatchReview(result tasks.GitHubResult, dark bool) *patchReview {
	review := &patchReview{result: result, patch: parsePatchDisplay(result.Patch), dark: dark}
	title := result.Heading
	if result.Repo != "" {
		title += " · " + result.Repo
	}
	review.window = mygo.NewWindow(mygo.WindowOptions{Title: title,
		Width: 1180, Height: 760, MinWidth: 780, MinHeight: 500, Content: ui.View(review.view)})
	patchReviewWindows[review.window] = struct{}{}
	review.window.OnClosed(func() {
		delete(patchReviewWindows, review.window)
		if review.media.cancel != nil {
			review.media.cancel()
		}
	})
	return review
}

func closePatchReviews() {
	for window := range patchReviewWindows {
		window.Close()
	}
}

func (r *patchReview) view(c *ui.Context) {
	appearance := "light"
	if r.dark {
		appearance = "dark"
	}
	c.SetTheme(themeFor(appearance))
	ui.Column(c).Fill().Background(c.Theme().Background).Children(func() {
		ui.Column(c).FillWidth().Padding(14, 18).Gap(5).Children(func() {
			ui.Text(c, r.result.Heading).FontSize(18).FontWeight(650)
			ui.Row(c).FillWidth().Gap(10).Wrap().Children(func() {
				ui.Text(c, r.result.Repo).FontSize(12).TextColor(c.Theme().TextMuted)
				if r.result.BaseSHA != "" {
					ui.Text(c, "Base "+shortSHA(r.result.BaseSHA)).FontSize(12).Tooltip(r.result.BaseSHA)
				}
				if r.result.HeadSHA != "" {
					ui.Text(c, "Head "+shortSHA(r.result.HeadSHA)).FontSize(12).Tooltip(r.result.HeadSHA)
				}
				if validGitHubLink(r.result.URL) {
					services := c.Services()
					ui.Button(c, "Open on GitHub").OnClick(func() { services.OpenURL(r.result.URL) })
				}
				ui.Button(c.Key("review-export"), "Export…").PopoverMenuButton(func(menu *ui.Menu) {
					r.media.menu(menu, r.window, context.Background(), exportDocument{
						title: r.result.Heading, text: r.result.Patch, dark: c.Theme().Dark})
				})
			})
			if r.result.More {
				ui.Text(c, "This is a partial preview. Open GitHub to inspect all changes.").FontSize(12).TextColor(c.Theme().TextMuted)
			}
		})
		r.media.view(c)
		if len(r.patch.files) == 0 {
			if r.raw.Buffer == nil {
				r.raw = editor.State{Buffer: editor.NewBuffer(r.result.Patch), ReadOnly: true, SoftWrap: true, Label: "Patch source"}
			}
			editor.View(c, &r.raw).Fill().MinHeight(0)
			return
		}
		ui.Row(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Scroll(c.Key("review-files")).Role(ui.RoleList).Width(230).MinHeight(0).FillHeight().Padding(8).Gap(3).
				BorderWidth(0, 1, 0, 0).BorderColor(c.Theme().Border).Children(func() {
				ui.Text(c, fmt.Sprintf("%d changed files", len(r.patch.files))).FontSize(12).Padding(6)
				for i, file := range r.patch.files {
					name := patchFileName(file)
					button := ui.ButtonBase(c.Key(i)).Role(ui.RoleListItem).FillWidth().Padding(8).Radius(6).Selected(r.selected == i).
						Label(name).Tooltip(name).OnClick(func() { r.selected = i })
					if r.selected == i || button.Hovered() {
						button.Background(c.Theme().SurfacePressed)
					}
					button.Children(func() { ui.Text(c, name).SingleLine().TruncateStart().FontSize(12).TextColor(c.Theme().Text) })
				}
			})
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Children(func() { r.fileView(c) })
		})
	})
}

func (r *patchReview) fileView(c *ui.Context) {
	view, file := &r.patch.views[r.selected], r.patch.files[r.selected]
	view.Layout = editor.DiffUnified
	if r.patch.split {
		view.Layout = editor.DiffSplit
	}
	ui.Row(c).FillWidth().Padding(6, 12).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Text(c, patchFileName(file)).SingleLine().TruncateStart().Grow(1).MinWidth(0).FontSize(12)
		added, deleted := view.Stats()
		ui.Text(c, fmt.Sprintf("+%d −%d", added, deleted)).FontSize(12).TextColor(c.Theme().TextMuted)
		fileMode(c.Key("review-unified"), "Unified", !r.patch.split, func() { r.patch.split = false })
		fileMode(c.Key("review-split"), "Side by side", r.patch.split, func() { r.patch.split = true })
		ui.Button(c.Key("review-find"), "Find").OnClick(func() { view.Run(editor.Find) })
		ui.Button(c.Key("review-next"), "Next change").Disabled(len(view.Hunks()) == 0).
			OnClick(func() { nextDiffChange(view) })
	})
	if len(file.Hunks) == 0 {
		text := "File metadata changed"
		if file.Binary {
			text = "Binary file changed"
		}
		ui.Text(c, text).Padding(16).TextColor(c.Theme().TextMuted)
		return
	}
	editor.DiffView(c.Key(r.selected), view).Fill().MinWidth(0).MinHeight(0)
}

func shortSHA(value string) string {
	return value[:min(12, len(value))]
}

func nextDiffChange(view *editor.DiffState) {
	if count := len(view.Hunks()); count != 0 {
		i, _ := view.CurrentHunk()
		view.GoToHunk((i + 1) % count)
	}
}
