package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"buddy/internal/tasks"
	"github.com/egoist/mygo/plugins/render"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const reviewFixture = "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n" +
	"diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-before\n+after\n"

func TestPatchReviewNavigatesFilesAndKeepsThePatchReadOnly(t *testing.T) {
	r := &patchReview{result: tasks.GitHubResult{Repo: "example/buddy", Heading: "#7 Quick fix", Patch: reviewFixture,
		BaseSHA: "1111111111111111111111111111111111111111", HeadSHA: "2222222222222222222222222222222222222222", More: true},
		patch: parsePatchDisplay(reviewFixture)}
	view := ui.NewTester(r.view, 1180, 760)
	if !view.HasText("2 changed files") || !view.HasText("Base 111111111111") ||
		!view.HasText("Head 222222222222") || !view.HasText("This is a partial preview. Open GitHub to inspect all changes.") {
		t.Fatal("review hides file navigation, version identity, or the partial-preview warning")
	}
	if err := view.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	if r.selected != 1 {
		t.Fatal("file navigation did not select the other patch")
	}
	if err := view.Click("Side by side"); err != nil {
		t.Fatal(err)
	}
	view.Frame()
	saveUIProof(t, "review.png", view)
	if r.patch.views[1].Layout != editor.DiffSplit || r.patch.views[1].Editable || r.patch.source != reviewFixture {
		t.Fatal("review changed the patch or did not switch layouts")
	}
}

func TestPatchReviewFallsBackToReadOnlySource(t *testing.T) {
	r := &patchReview{dark: true, result: tasks.GitHubResult{Heading: "Partial patch", Patch: "+just a snippet"}}
	view := ui.NewTester(r.view, 1000, 650)
	if field, ok := view.AccessByRole(ui.RoleTextField, "Patch source"); !ok || !field.ReadOnly {
		t.Fatal("unparsed patch source is missing or editable")
	}
	if r.raw.Buffer.String() != "+just a snippet" {
		t.Fatal("source preview changed the patch")
	}
}

func TestPatchReviewDemo(t *testing.T) {
	dir := os.Getenv("BUDDY_UI_PROOF_DIR")
	if dir == "" || runtime.GOOS != "darwin" {
		t.Skip("set BUDDY_UI_PROOF_DIR on macOS to render a native UI demo")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	comp := render.Composition{Width: 1920, Height: 1200, Scale: 2, FPS: 24, Frames: 144, Dark: true,
		View: func(c *ui.Context, frame render.Frame) {
			container := ui.Column(c).Fill()
			review := ui.Local(container, "review-demo", func() patchReview {
				return patchReview{dark: true, result: tasks.GitHubResult{Repo: "example/buddy", Heading: "#7 Quick fix"},
					patch: parsePatchDisplay(reviewFixture)}
			})
			review.patch.split = frame.Index >= 48
			review.selected = min(1, frame.Index/96)
			container.Children(func() { review.view(c) })
		}}
	if err := render.Video(t.Context(), comp, filepath.Join(dir, "review-demo.mp4"), render.Options{Renderer: render.CPU}); err != nil {
		t.Fatal(err)
	}
}
