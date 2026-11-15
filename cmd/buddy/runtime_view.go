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
		if modelPicker(c.Key("default-model-picker"), &a.edit.Model, a.models) {
			preferences.Effort, preferences.Speed = "", ""
		}
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

func modelChoices(models []tasks.Model) []tasks.ModelSpeed {
	seen, names := map[string]bool{}, map[string]int{}
	options := []tasks.ModelSpeed{}
	for _, model := range models {
		if seen[model.ID] || model.ID == "" {
			continue
		}
		seen[model.ID] = true
		name := model.Name
		if name == "" {
			name = model.ID
		}
		options = append(options, tasks.ModelSpeed{ID: model.ID, Name: name})
		names[name]++
	}
	for i := range options {
		if names[options[i].Name] > 1 {
			options[i].Name += " · " + options[i].ID
		}
	}
	return options
}

// One editable picker supports both the catalog and a manually entered model ID.
func modelPicker(c *ui.Context, value *string, models []tasks.Model) bool {
	options := modelChoices(models)
	display := func(id string) string {
		if id == "" {
			id = tasks.DefaultCodexModel
		}
		for _, option := range options {
			if option.ID == id {
				return option.Name
			}
		}
		return id
	}
	before := *value
	ui.Box(c).Width(290).MaxWidth(320).MinWidth(0).AlignSelf(ui.Start).Children(func() {
		box := ui.Row(c).FillWidth()
		draft := ui.Local(box, "model-draft", func() string { return display(*value) })
		last := ui.Local(box, "model-value", func() string { return *value })
		if *last != *value {
			*draft, *last = display(*value), *value
		}
		box.Children(func() {
			picker := ui.ComboboxBase(c.Key("model-input"), draft)
			picker.Input.Label("Choose default model").Placeholder(tasks.DefaultCodexModel).FillWidth().Height(34).
				Padding(0, 29, 0, 10).FontSize(12).Radius(8).Border(1, c.Theme().Border).Background(c.Theme().Surface)
			if !picker.Input.Focused() && !picker.Input.Changed() {
				*draft = display(*value)
			}
			if picker.Input.Focused() {
				picker.Input.BorderColor(c.Theme().Accent)
			}
			if picker.Input.Changed() {
				*value = strings.TrimSpace(*draft)
				for _, option := range options {
					if strings.EqualFold(*value, option.Name) {
						*value = option.ID
						break
					}
				}
				*last = *value
			}
			picker.Popup(func(menu ui.Element) {
				menu.Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(9).Padding(4)
				ui.Scroll(c).FillWidth().MaxHeight(280).Children(func() {
					query := strings.ToLower(strings.TrimSpace(*draft))
					matches := 0
					for _, option := range options {
						if picker.Filtering() && !strings.Contains(strings.ToLower(option.ID+" "+option.Name), query) {
							continue
						}
						matches++
						item := picker.Item(option.ID).Label(option.Name).Padding(8, 10).Radius(6)
						if item.Highlighted() || *value == option.ID {
							item.Background(c.Theme().SurfacePressed)
						}
						item.Children(func() { ui.Text(c, option.Name).FontSize(12).SingleLine() })
					}
					if matches == 0 {
						ui.Text(c, "Enter a model ID").FontSize(12).TextColor(c.Theme().TextMuted).Padding(8, 10)
					}
				})
			})
			if choice, chosen := picker.Chosen(); chosen {
				*value, *last, *draft = choice, choice, display(choice)
			}
			ui.Icon(c, chevronDownMark).Size(12, 12).Absolute().Right(10).Top(11).TextColor(c.Theme().TextMuted).IgnoreInput(true)
		})
	})
	return before != *value
}

var chevronDownMark = svg(`<path d="m7 10 5 5 5-5"/>`)

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
	selectOption.Trigger.Label(label).Height(34).MinWidth(170).MaxWidth(290).AlignSelf(ui.Start).Justify(ui.Start).
		Padding(0, 10).Gap(12).Radius(8).Border(1, c.Theme().Border).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, title).FontSize(12).SingleLine().MinWidth(0)
		ui.Icon(c, chevronDownMark).Size(12, 12).Shrink(0).TextColor(c.Theme().TextMuted)
	})
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
