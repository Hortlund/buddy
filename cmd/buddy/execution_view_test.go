package main

import (
	"errors"
	"testing"

	"buddy/internal/api"
	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func TestExecutionRefreshPreservesActionFailure(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.ExecutionProtocol}
	a.execution.err = "This checkout is unavailable. Choose another folder."
	a.execution.finishRefresh(nil, nil, tasks.ExecutionConfig{}, nil, errors.New("offline"))
	view := ui.NewTester(a.executionSettings, 850, 650)
	if !view.HasText(a.execution.err) || !view.HasText("Couldn't refresh hosts and projects.") {
		t.Fatal("action or refresh error was hidden", view.Texts())
	}
	a.execution.finishRefresh([]tasks.Device{{ID: "server", Name: "Buddy server", Online: true}}, nil, tasks.ExecutionConfig{}, nil, nil)
	view.Frame()
	if !view.HasText(a.execution.err) || view.HasText("Couldn't refresh hosts and projects.") || !view.HasText("Buddy server") {
		t.Fatal("successful refresh cleared the action failure or kept the stale load error", view.Texts())
	}
}
