package main

import (
	"fmt"
	"strings"
	"time"

	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var terminalMark = svg(`<rect x="3" y="5" width="18" height="14" rx="3"/><path d="m7 9 3 3-3 3m6 0h4"/>`)

type codeOutputView struct {
	open, loading bool
	chat, task    string
	output        *tasks.ChatCodeRun
	nextPoll      time.Time
	request       uint64
	err           string
}

func codeRunStatus(run *tasks.ChatCodeRun, stopping bool) string {
	switch run.Status {
	case tasks.Succeeded:
		return "Finished"
	case tasks.Failed:
		return "Failed"
	case tasks.Cancelled:
		return "Stopped"
	case tasks.Interrupted:
		return "Interrupted"
	default:
		if stopping {
			return "Stopping"
		}
		return "Running"
	}
}

func (a *desktop) codeRunCard(c *ui.Context, id string, run *tasks.ChatCodeRun, stopping bool) {
	chat := a.currentConversation()
	if run.WorkCommand && a.section == "work" && a.work.selected.ID == id && a.work.selected.ConversationID != "" {
		chat = a.work.selected.ConversationID
	}
	if run.RunID != "" {
		id += "/" + run.RunID
	}
	theme := c.Theme()
	label, tint := codeRunStatus(run, stopping), theme.Accent
	switch run.Status {
	case tasks.Succeeded:
		tint = theme.Success
	case tasks.Failed:
		tint = theme.Danger
	case tasks.Cancelled:
		tint = theme.TextMuted
	case tasks.Interrupted:
		tint = theme.Warning
	}
	end := c.Now()
	if run.FinishedAt != nil {
		end = *run.FinishedAt
	}
	elapsed := max(0, end.Sub(run.StartedAt))
	if run.Status == tasks.Running {
		c.After(time.Second - elapsed%time.Second)
	}
	if run.ExitCode != nil {
		label += fmt.Sprintf(" · Exit %d", *run.ExitCode)
	}
	card := ui.Column(c.Key("run-card")).MinWidth(0).MaxWidth(586).AlignSelf(ui.Start).
		FillWidth().Gap(8).Padding(10, 12).Radius(12).Background(theme.Surface).Border(1, theme.Border)
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(7).Children(func() {
			ui.Icon(c, terminalMark).Size(15, 15).TextColor(theme.TextMuted)
			title := "Code run"
			if run.WorkCommand {
				title = "Command"
			}
			ui.Text(c, title).FontSize(12).FontWeight(650)
			ui.Box(c).Grow(1)
			if run.Status == tasks.Running {
				ui.Spinner(c).Size(12, 12).TextColor(tint)
			}
			ui.Text(c, label).FontSize(11).FontWeight(600).TextColor(tint)
			ui.Text(c, fmt.Sprintf("%ds", int(elapsed/time.Second))).FontSize(11).TextColor(theme.TextMuted)
		})
		if run.Command != "" {
			ui.Text(c, run.Command).MinWidth(0).Font("monospace").FontSize(12).Selectable()
		}
		if run.Error != "" {
			ui.Text(c, run.Error).FontSize(12).TextColor(theme.Danger).Selectable()
		}
		if run.Stdout != "" || run.Stderr != "" || run.OutputPreview {
			ui.Column(c).FillWidth().Gap(7).Padding(8).Radius(7).Background(theme.SurfacePressed).Children(func() {
				runOutputPreview(c, "Output", run.Stdout)
				runOutputPreview(c, "Standard error", run.Stderr)
			})
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
				if run.Truncated {
					ui.Text(c, "Output truncated").FontSize(11).TextColor(theme.TextMuted)
				} else if run.OutputPreview {
					ui.Text(c, "Output preview").FontSize(11).TextColor(theme.TextMuted)
				}

				ui.Box(c).Grow(1)
				ui.Button(c.Key("view-run-output"), "View output").Height(25).OnClick(func() {
					a.showCodeOutput(chat, id, run)
				})
				ui.Button(c.Key("copy-run-output"), "Copy").Label("Copy output").Height(25).OnClick(func() {
					if !run.OutputPreview {
						mygo.Clipboard.WriteText(runOutputText(run))
					} else {
						a.copyCodeOutput(chat, id)
					}
				})
			})
		} else if run.Status == tasks.Running {
			ui.Text(c, "Waiting for output…").FontSize(11).TextColor(theme.TextMuted)
		}
	})
}

func (a *desktop) copyCodeOutput(chat, id string) {
	if a.window == nil {
		return
	}
	win, client := a.window, a.client
	go func() {
		output, err := client.ChatCodeOutput(a.ctx, chat, id)
		win.Update(func() {
			if a.currentConversation() != chat && (a.section != "work" || a.work.selected.ConversationID != chat) {
				return
			}
			if err != nil {
				a.chatErr = errorMessage(err, "Couldn't copy the run output.")
				return
			}
			mygo.Clipboard.WriteText(runOutputText(&output))
		})
	}()
}

func (a *desktop) showCodeOutput(chat, id string, run *tasks.ChatCodeRun) {
	a.codeOutput = codeOutputView{open: true, chat: chat, task: id, request: a.codeOutput.request + 1}
	if !run.OutputPreview {
		a.codeOutput.output = run
	}
}

func (a *desktop) codeOutputDialog(c *ui.Context) {
	state := &a.codeOutput
	if !state.open || (a.section != "work" && state.chat != a.currentConversation()) {
		return
	}
	if !state.loading && (state.output == nil || state.output.Status == tasks.Running) {
		if delay := state.nextPoll.Sub(c.Now()); delay > 0 {
			c.After(delay)
		} else if state.err == "" || state.output != nil {
			a.loadCodeOutput()
		}
	}
	ui.Modal(c, &state.open, func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Run output").Role(ui.RoleHeading).Bold()
			ui.Box(c).Grow(1)
			if output := state.output; output != nil {
				label := codeRunStatus(output, false)
				if output.ExitCode != nil {
					label += fmt.Sprintf(" · Exit %d", *output.ExitCode)
				}
				ui.Text(c, label).Role(ui.RoleStatus).TextColor(c.Theme().TextMuted)
			}
		})
		ui.Scroll(c.Key("log")).Width(480).MaxWidth(480).MaxHeight(350).Gap(9).Children(func() {
			if output := state.output; output != nil {
				if output.Error != "" {
					ui.Text(c, output.Error).TextColor(c.Theme().Danger).Selectable()
				}
				runOutput(c, "Output", output.Stdout, 0)
				runOutput(c, "Standard error", output.Stderr, 0)
				if output.Truncated {
					ui.Text(c, "Earlier output omitted").FontSize(11).TextColor(c.Theme().TextMuted)
				}
			} else if state.err == "" {
				ui.Text(c, "Loading output…")
			}
			if state.err != "" {
				ui.Text(c, state.err).FontSize(11).TextColor(c.Theme().Danger)
			}
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if state.err != "" {
				ui.Button(c.Key("retry"), "Retry").Disabled(state.loading).OnClick(a.loadCodeOutput)
			}
			ui.Button(c.Key("copy"), "Copy output").Disabled(state.output == nil).
				OnClick(func() { mygo.Clipboard.WriteText(runOutputText(state.output)) })
			ui.Button(c.Key("done"), "Done").OnClick(func() { state.open = false })
		})
	})
}

func (a *desktop) loadCodeOutput() {
	state := &a.codeOutput
	if state.loading || a.window == nil {
		return
	}
	state.loading, state.err = true, ""
	chat, id, request, win, client := state.chat, state.task, state.request, a.window, a.client
	go func() {
		output, err := client.ChatCodeOutput(a.ctx, chat, id)
		win.Update(func() {
			if state.chat != chat || state.task != id || state.request != request {
				return
			}
			state.loading = false
			state.nextPoll = time.Now().Add(500 * time.Millisecond)
			if err != nil {
				state.err = errorMessage(err, "Couldn't load the run output.")
				return
			}
			state.output, state.err = &output, ""
		})
	}()
}

func runOutputPreview(c *ui.Context, title, output string) {
	if output == "" {
		return
	}
	lines := strings.Split(strings.TrimRight(output, "\r\n"), "\n")
	if len(lines) > 6 {
		title += " · Last 6 lines"
		lines = lines[len(lines)-6:]
	}
	runOutput(c, title, strings.Join(lines, "\n"), 6)
}

func runOutput(c *ui.Context, title, output string, maxLines int) {
	if output == "" {
		return
	}
	ui.Text(c, title).FontSize(11).TextColor(c.Theme().TextMuted)
	ui.Text(c, output).MinWidth(0).Font("monospace").FontSize(11).MaxLines(maxLines).Selectable()
}

func runOutputText(run *tasks.ChatCodeRun) string {
	if run.Stderr == "" {
		return run.Stdout
	}
	if run.Stdout == "" {
		return run.Stderr
	}
	return run.Stdout + "\n\nStandard error\n" + run.Stderr
}
