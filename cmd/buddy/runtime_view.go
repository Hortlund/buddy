package main

import (
	"strings"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func (a *desktop) modelSettings(c *ui.Context) {
	if !a.protocolChecked.IsZero() && a.serverProtocol < api.AutomaticWorkerModelsProtocol {
		ui.TextInput(c.Key("agent-model"), &a.edit.Model).Label("Codex model ID").Placeholder(tasks.DefaultCodexModel).Height(36)
		ui.Text(c, "Update the Buddy server for automatic worker settings.").FontSize(12).TextColor(c.Theme().TextMuted)
		return
	}
	if a.edit.ModelPreferences == nil {
		a.edit.ModelPreferences = &tasks.ModelPreferences{Mode: "auto"}
	}
	preferences := a.edit.ModelPreferences
	if preferences.Mode == "" {
		preferences.Mode = "auto"
	}
	ui.Column(c).Gap(9).Children(func() {
		ui.Text(c, "Worker intelligence").FontWeight(650)
		modelOption(c.Key("worker-model-mode"), &preferences.Mode, "Let Bud decide", "Worker settings", []tasks.ModelSpeed{
			{ID: "auto", Name: "Let Bud decide"},
			{ID: "fixed", Name: "Use fixed settings"},
		})
		if preferences.Mode == "auto" {
			ui.Text(c, "Each worker chooses its model and effort for the task using the models available on its machine. Chat uses your default model.").FontSize(12).TextColor(c.Theme().TextMuted)
		}
		ui.Text(c, "Default model").FontSize(12).FontWeight(600)
		ui.Row(c).Gap(7).Children(func() {
			input := ui.TextInput(c.Key("agent-model"), &a.edit.Model).Label("Default model ID").Placeholder(tasks.DefaultCodexModel).Height(33).Grow(1).MinWidth(0)
			if input.Changed() {
				preferences.Effort, preferences.Speed = "", ""
			}
			options := []tasks.ModelSpeed{}
			for _, model := range a.models {
				name := model.Name
				if name == "" {
					name = model.ID
				}
				options = append(options, tasks.ModelSpeed{ID: model.ID, Name: name})
			}
			if len(options) != 0 && modelOption(c.Key("default-model-picker"), &a.edit.Model, "Choose model", "Choose default model", options) {
				preferences.Effort, preferences.Speed = "", ""
			}
		})
		if preferences.Mode == "fixed" {
			efforts := []tasks.ModelSpeed{{ID: "", Name: "Model default"}}
			speeds := []tasks.ModelSpeed{{ID: "", Name: "Account default"}}
			selected := a.edit.Model
			if selected == "" {
				selected = tasks.DefaultCodexModel
			}
			for _, model := range a.models {
				if model.ID != selected {
					continue
				}
				for _, effort := range model.Efforts {
					efforts = append(efforts, tasks.ModelSpeed{ID: effort, Name: effort})
				}
				speeds = append(speeds, model.Speeds...)
			}
			ui.Row(c).Gap(7).Children(func() {
				modelOption(c.Key("default-effort"), &preferences.Effort, "Model default", "Default reasoning effort", efforts)
				modelOption(c.Key("default-speed"), &preferences.Speed, "Account default", "Default speed", speeds)
			})
			ui.Text(c, "Chat and workers use these settings until you let the Bud decide again.").FontSize(12).TextColor(c.Theme().TextMuted)
		}
		if a.modelsLoading {
			ui.Text(c, "Checking available Codex models…").FontSize(12).TextColor(c.Theme().TextMuted)
		}
	})
}

func modelOption(c *ui.Context, value *string, empty, label string, options []tasks.ModelSpeed) bool {
	selectOption := ui.SelectBase(c, value)
	title := *value
	if title == "" {
		title = empty
	}
	for _, option := range options {
		if option.ID == *value && *value != "" {
			title = option.Name
		}
	}
	if strings.HasSuffix(label, "reasoning effort") {
		title = "Effort: " + title
	}
	if strings.HasSuffix(label, "speed") {
		title = "Speed: " + title
	}
	selectOption.Trigger.Label(label).Height(31).Padding(0, 10).Radius(7).
		Border(1, c.Theme().Border).Background(c.Theme().Surface).Children(func() { ui.Text(c, title+" ▾").FontSize(12) })
	selectOption.Popup(func(menu ui.Element) {
		menu.Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(8).Padding(4).MinWidth(160)
		for _, option := range options {
			item := selectOption.Item(option.ID).Label(option.Name).Padding(7, 9).Radius(5)
			if item.Highlighted() || *value == option.ID {
				item.Background(c.Theme().SurfacePressed).TextColor(c.Theme().Text)
			}
			item.Children(func() { ui.Text(c, option.Name).SingleLine() })
		}
	})
	return selectOption.Trigger.Changed()

}

var runtimeMark = svg(`<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="m7 9 3 3-3 3m5 0h4"/>`)

func (a *desktop) runtimes(c *ui.Context) {
	sectionLabel(c, "Runtimes")
	connectionSection(c, func() {
		if a.online && !a.protocolChecked.IsZero() && a.serverProtocol < api.DesktopProtocol {
			ui.Text(c, "Update the Buddy server to use the latest features.").
				Padding(9, 13).FontSize(12).TextColor(c.Theme().TextMuted)
		}
		ui.Row(c).Padding(11, 13).Gap(9).Children(func() {
			label, detail, ready := "Checking", "Chat and background work", false
			switch {
			case !a.online:
				label = "Server offline"
			case a.auth.State == "" && !a.protocolChecked.IsZero() && a.serverProtocol < api.AttachmentProtocol:
				label = "Update server"
			case a.auth.State == "missing":
				label, detail = "Install required", "Install the Codex CLI on the server"
			case a.auth.State == "signed_in":
				label, detail, ready = "Active", "Signed in with the server’s Codex CLI", true
			case a.auth.State == "configured":
				label, detail, ready = "Configured", "OpenAI API key", true
				if a.auth.Source == "environment_key" {
					detail = "API key from the server environment"
				}
			case a.auth.State == "sign_in_required":
				label, detail = "Available", "Sign in to activate Codex"
			case a.auth.State == "unknown" || a.authErr != "":
				label = "Status unavailable"
			}
			connectionDetail(c, runtimeMark, "Codex", detail)
			connectionStatus(c, label, ready)
			action := "Manage"
			if label == "Available" || label == "Install required" {
				action = "Set up"
			}
			if a.authOpen {
				action = "Done"
			}
			ui.Button(c.Key("manage-codex"), action).Height(30).OnClick(func() {
				a.authOpen = !a.authOpen
				if a.authOpen {
					a.changeAuth("read")
				} else {
					a.authDraft = ""
				}
			})
		})
		if a.authOpen {
			a.codexConnection(c)
		}
		if a.serverProtocol >= api.ExecutionProtocol {
			ui.Row(c).Padding(11, 13).Gap(9).Children(func() {
				configured := a.online && (a.auth.Source == "saved_key" || a.auth.Source == "environment_key")
				label := "API key needed"
				if configured {
					label = "Configured"
				}
				connectionDetail(c, pictureMark, "Image generation", "")
				connectionStatus(c, label, configured)
			})
		}
		a.voiceSettings(c)
	})
}

func (a *desktop) codexConnection(c *ui.Context) {
	ui.Column(c).Padding(10, 13, 12).Gap(8).Children(func() {
		detail := "Sign in with codex login on the server, or save an OpenAI API key here."
		if a.auth.State == "missing" {
			detail = "Install the Codex CLI on the server, then restart Buddy there."
		} else if a.auth.Source == "environment_key" {
			detail = "A saved key overrides the server environment key."
		}
		ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c.Key("codex-api-key"), &a.authDraft).Password().Label("OpenAI API key").
				Placeholder("OpenAI API key").Height(31).Grow(1).MinWidth(0)
			ui.PrimaryButton(c.Key("codex-api-key-save"), "Save key").Height(31).
				Disabled(!a.online || a.authBusy || strings.TrimSpace(a.authDraft) == "").OnClick(func() { a.changeAuth("save") })
			if a.auth.Source == "saved_key" {
				ui.Button(c.Key("codex-api-key-remove"), "Remove key").Height(31).
					Disabled(!a.online || a.authBusy).OnClick(func() { a.changeAuth("remove") })
			}
		})
		if a.authErr != "" {
			ui.Text(c, a.authErr).FontSize(12).TextColor(c.Theme().Danger)
		}
	})
}
