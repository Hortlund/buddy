package main

import (
	"strings"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func (a *desktop) modelSettings(c *ui.Context) {
	if !a.protocolChecked.IsZero() && a.serverProtocol < api.ModelProfilesProtocol {
		ui.TextInput(c.Key("agent-model"), &a.edit.Model).Label("Codex model ID").Placeholder(tasks.DefaultCodexModel).Height(36)
		ui.Text(c, "Update the Buddy server to configure model profiles.").FontSize(12).TextColor(c.Theme().TextMuted)
		return
	}
	if a.edit.ModelPreferences == nil {
		a.edit.ModelPreferences = &tasks.ModelPreferences{}
	}
	preferences := a.edit.ModelPreferences
	for _, kind := range tasks.TaskProfileKinds {
		index := -1
		for i := range preferences.Profiles {
			if preferences.Profiles[i].Kind == kind {
				index = i
				break
			}
		}
		if index < 0 {
			preferences.Profiles = append(preferences.Profiles, tasks.TaskModelProfile{Kind: kind})
			index = len(preferences.Profiles) - 1
		}
	}
	ui.Column(c).Gap(9).Children(func() {
		ui.Text(c, "Codex · task profiles").FontWeight(650)
		a.modelProfileFields(c, "Default", &a.edit.Model, &preferences.Effort, &preferences.Speed, false)
		label := "Codex chooses a configured profile for each request. Empty profiles use your default."
		if a.modelsLoading {
			label = "Checking Codex models, effort levels and speed options…"
		}
		ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted)
		for index := range preferences.Profiles {
			profile := &preferences.Profiles[index]
			kind := profile.Kind
			name := strings.ToUpper(kind[:1]) + kind[1:]
			a.modelProfileFields(c.Key("profile-"+kind), name, &profile.Model, &profile.Effort, &profile.Speed, true)
		}
		ui.Text(c, "Choose high effort for coding and review, and a lighter model for mail or research. Speed options depend on your Codex account and model.").FontSize(12).TextColor(c.Theme().TextMuted)
	})
}

func (a *desktop) modelProfileFields(c *ui.Context, label string, model, effort, speed *string, inherit bool) {
	prefix := strings.ToLower(label)
	modelKey := prefix + "-model"
	if !inherit {
		modelKey = "agent-model"
	}
	ui.Column(c).Gap(5).Children(func() {
		ui.Text(c, label).FontSize(12).FontWeight(600)
		ui.Row(c).Gap(7).Children(func() {
			placeholder := tasks.DefaultCodexModel
			if inherit {
				placeholder = "Use default model"
			}
			input := ui.TextInput(c.Key(modelKey), model).Label(label + " model ID").Placeholder(placeholder).Height(33).Grow(1).MinWidth(0)
			if input.Changed() {
				*effort, *speed = "", ""
			}
			options := []tasks.ModelSpeed{}
			if inherit {
				options = append(options, tasks.ModelSpeed{ID: "", Name: "Use default"})
			}
			for _, choice := range a.models {
				name := choice.Name
				if name == "" {
					name = choice.ID
				}
				options = append(options, tasks.ModelSpeed{ID: choice.ID, Name: name})
			}
			if len(options) != 0 {
				if modelOption(c.Key(prefix+"-model-picker"), model, "Choose model", label+" model", options) {
					*effort, *speed = "", ""
				}
			}
		})
		var efforts, speeds []tasks.ModelSpeed
		efforts = append(efforts, tasks.ModelSpeed{ID: "", Name: "Model default"})
		speeds = append(speeds, tasks.ModelSpeed{ID: "", Name: "Account default"})
		selected := *model
		if selected == "" {
			selected = a.edit.Model
		}
		if selected == "" {
			selected = tasks.DefaultCodexModel
		}
		for _, choice := range a.models {
			if choice.ID != selected {
				continue
			}
			for _, value := range choice.Efforts {
				efforts = append(efforts, tasks.ModelSpeed{ID: value, Name: value})
			}
			speeds = append(speeds, choice.Speeds...)
		}
		ui.Row(c).Gap(7).Children(func() {
			effortChanged := modelOption(c.Key(prefix+"-effort"), effort, "Model default", label+" reasoning effort", efforts)
			speedChanged := modelOption(c.Key(prefix+"-speed"), speed, "Account default", label+" speed", speeds)
			if inherit && *model == "" && (effortChanged || speedChanged) {
				*model = selected
			}
		})
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
