package main

import (
	"testing"

	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func TestTaskProfileChoicesStayWithTheirTask(t *testing.T) {
	a := &desktop{edit: tasks.Agent{Model: "default-model"}, models: []tasks.Model{
		{ID: "default-model", Name: "Default model", Efforts: []string{"low", "high"}},
		{ID: "strong-model", Name: "Strong model", Efforts: []string{"high", "xhigh"}, Speeds: []tasks.ModelSpeed{{ID: "priority", Name: "Fast"}}},
	}}
	view := ui.NewTester(a.modelSettings, 800, 1200)
	if err := view.Click("Coding model"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Strong model"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Coding reasoning effort"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Coding speed"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Fast"); err != nil {
		t.Fatal(err)
	}
	var coding tasks.TaskModelProfile
	for _, profile := range a.edit.ModelPreferences.Profiles {
		if profile.Kind == "coding" {
			coding = profile
		} else if profile.Model != "" {
			t.Fatalf("selection changed another task: %+v", profile)
		}
	}
	if coding.Model != "strong-model" || coding.Effort != "xhigh" || coding.Speed != "priority" || a.edit.Model != "default-model" {
		t.Fatalf("profile choices: %+v", a.edit.ModelPreferences)
	}
	if err := view.Click("Coding model"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Default model"); err != nil {
		t.Fatal(err)
	}
	for _, profile := range a.edit.ModelPreferences.Profiles {
		if profile.Kind == "coding" && (profile.Effort != "" || profile.Speed != "") {
			t.Fatal("changing the model retained potentially unsupported options")
		}
	}
}
