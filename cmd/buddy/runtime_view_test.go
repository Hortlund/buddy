package main

import (
	"testing"

	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func TestAutomaticWorkerSettingsAndFixedOverride(t *testing.T) {
	a := &desktop{edit: tasks.Agent{Model: "default-model"}, models: []tasks.Model{
		{ID: "default-model", Name: "Default model", Efforts: []string{"low", "high"}},
		{ID: "strong-model", Name: "Strong model", Efforts: []string{"high", "xhigh"}, Speeds: []tasks.ModelSpeed{{ID: "priority", Name: "Fast"}}},
	}}
	view := ui.NewTester(a.modelSettings, 800, 1200)
	if a.edit.ModelPreferences == nil || a.edit.ModelPreferences.Mode != "auto" {
		t.Fatal("workers should choose settings by default")
	}
	for _, label := range []string{"Worker settings", "Choose default model"} {
		if control, ok := view.Find(label); !ok || control.W > 320 {
			t.Fatalf("%s is not compact: %+v", label, control)
		}
	}
	if _, found := view.Find("Default model ID"); found {
		t.Fatal("model picker still has a duplicate ID field")
	}
	if err := view.Click("Worker settings"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Use fixed settings"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Choose default model"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Strong model"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Default reasoning effort"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Default speed"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Fast"); err != nil {
		t.Fatal(err)
	}
	p := a.edit.ModelPreferences
	if p.Mode != "fixed" || p.Effort != "xhigh" || p.Speed != "priority" || a.edit.Model != "strong-model" {
		t.Fatalf("fixed settings: %+v", p)
	}
	if err := view.Click("Choose default model"); err != nil {
		t.Fatal(err)
	}
	option, ok := view.AccessByRole(ui.RoleListItem, "Default model")
	if !ok {
		t.Fatal("default model is missing from the picker")
	}
	view.ClickAt(option.Bounds.X+option.Bounds.W/2, option.Bounds.Y+option.Bounds.H/2)
	if p.Effort != "" || p.Speed != "" {
		t.Fatal("model change retained unsupported options")
	}
	if err := view.Click("Worker settings"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Let Bud decide"); err != nil {
		t.Fatal(err)
	}
	if p.Mode != "auto" {
		t.Fatal("automatic mode was not restored")
	}
	if err := view.Click("Default reasoning effort"); err == nil {
		t.Fatal("automatic mode still shows a fixed effort control")
	}
}
