//go:build e2e

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"buddy/internal/tasks"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Native app loops must run on the process's main thread. Keep this integration
// check in its own process and leave regular view tests headless.
func TestMain(m *testing.M) {
	if os.Getenv("BUDDY_TEST_REVIEW_LIFECYCLE") == "1" {
		if err := checkNativeReviewLifecycle(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestPatchReviewWindowLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native window integration runs on the macOS desktop target")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	command.Env = append(os.Environ(), "BUDDY_TEST_REVIEW_LIFECYCLE=1", "MYGO_AUTOMATION=")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Review lifecycle passed") {
		t.Fatalf("native window lifecycle: %v\n%s", err, output)
	}
}

func checkNativeReviewLifecycle() error {
	var failure error
	mygo.App.WhenReady(func() {
		view := ui.View(func(c *ui.Context) { ui.Text(c, "Disposable lifecycle fixture") })
		main := mygo.NewWindow(mygo.WindowOptions{Title: "Buddy fixture", Width: 820, Height: 590, Content: view})
		app := &desktop{window: main}
		app.bindMainWindowLifecycle()
		other := mygo.NewWindow(mygo.WindowOptions{Title: "Unrelated window", Width: 300, Height: 200, Content: view})
		first := openPatchReview(tasks.GitHubResult{Heading: "Review one", Patch: reviewFixture}, false)
		second := openPatchReview(tasks.GitHubResult{Heading: "Review two", Patch: reviewFixture}, false)
		cancelled := 0
		first.media.cancel = func() { cancelled++ }
		second.media.cancel = func() { cancelled++ }
		preventClose := true
		main.OnClose(func(e *mygo.CloseEvent) {
			if preventClose {
				e.PreventDefault()
			}
		})
		time.AfterFunc(100*time.Millisecond, func() {
			mygo.RunOnMain(func() {
				defer func() { preventClose = false; mygo.App.Quit() }()
				first.window.Close()
				if !first.window.IsDestroyed() || main.IsDestroyed() || len(patchReviewWindows) != 1 || cancelled != 1 {
					failure = fmt.Errorf("independent review close did not release only its own resources")
					return
				}
				main.Close()
				if main.IsDestroyed() || second.window.IsDestroyed() || cancelled != 1 {
					failure = fmt.Errorf("cancelled main-window close also closed a review")
					return
				}
				preventClose = false
				main.Close()
				if !main.IsDestroyed() || !second.window.IsDestroyed() || len(patchReviewWindows) != 0 || cancelled != 2 {
					failure = fmt.Errorf("accepted main-window close left a review or its export alive")
					return
				}
				if other.IsDestroyed() {
					failure = fmt.Errorf("review cleanup closed an unrelated window")
					return
				}
				fmt.Println("Review lifecycle passed")
			})
		})
	})
	if err := mygo.App.Run(); err != nil {
		return err
	}
	return failure
}
