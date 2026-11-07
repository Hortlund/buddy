package main

import (
	"strings"

	"buddy/internal/api"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

var settingsMark = svg(`<path d="M10 3h4l.6 2.4 2 .9 2.2-.9 2.1 3.5-1.7 1.6v2.4l1.7 1.6-2.1 3.5-2.2-.9-2 .9L14 21h-4l-.6-2.4-2-.9-2.2.9-2.1-3.5 1.7-1.6v-2.4L3.1 9l2.1-3.5 2.2.9 2-.9L10 3z"/><circle cx="12" cy="12" r="3"/>`)

var languageCodes = []string{"", "ar", "zh", "da", "nl", "en", "fi", "fr", "de", "el", "he", "hi", "id", "it", "ja", "ko", "no", "pl", "pt", "ro", "ru", "es", "sv", "th", "tr", "uk", "vi"}

type languagePicker struct {
	open   bool
	search string
}

func (a *desktop) showPreferences() {
	if a.saving {
		return
	}
	a.section = "preferences"
	a.preferencesErr = ""
	if !a.savingLanguages {
		a.loadedLanguages = false
	}
	a.refreshSoon()
}

func (a *desktop) loadLanguages() {
	if a.loadedLanguages || a.loadingLanguages || a.preferencesErr != "" || !a.online || a.serverProtocol < api.WorkbenchProtocol || a.window == nil {
		return
	}
	a.loadingLanguages = true
	client, win := a.client, a.window
	go func() {
		prefs, err := client.Languages(a.ctx)
		win.Update(func() {
			a.loadingLanguages = false
			if err != nil {
				a.preferencesErr = errorMessage(err, "Couldn't load language preferences.")
				return
			}
			a.languages = prefs
			a.loadedLanguages = true
		})
	}()
}

func (a *desktop) saveLanguages(prefs tasks.LanguagePreferences) {
	if a.savingLanguages || a.window == nil || !a.online {
		return
	}
	a.savingLanguages, a.preferencesErr = true, ""
	client, win := a.client, a.window
	go func() {
		saved, err := client.SaveLanguages(a.ctx, prefs)
		win.Update(func() {
			a.savingLanguages = false
			if err != nil {
				a.preferencesErr = errorMessage(err, "Couldn't save language preferences.")
				return
			}
			a.languages = saved
		})
	}()
}

func (a *desktop) preferences(c *ui.Context) {
	a.loadLanguages()
	controlPage(c.Key("preferences"), "Settings", "Choose how Buddy looks and which languages it uses.", nil, func() {
		if a.preferencesErr != "" {
			ui.Text(c, a.preferencesErr).TextColor(c.Theme().Danger)
		}
		controlSection(c, "Appearance", "", nil)
		connectionSection(c, func() {
			ui.Row(c).FillWidth().Padding(12).Gap(12).AlignItems(ui.Center).Children(func() {
				connectionDetail(c, deviceMark, "Theme", "Applies to this desktop")
				ui.Row(c).Shrink(0).Gap(2).Padding(3).Radius(8).Background(c.Theme().SurfaceHover).
					Border(1, c.Theme().Border).Children(func() {
					a.themeChoice(c, "Light", "light")
					a.themeChoice(c, "Dark", "dark")
				})
			})
		})
		controlSection(c, "Languages", "", nil)
		connectionSection(c, func() {
			a.languageSetting(c, "chat-language", "Chat", "Replies and background updates", a.languages.Chat, func(code string) {
				prefs := a.languages
				prefs.Chat = code
				a.saveLanguages(prefs)
			})
			a.languageSetting(c, "voice-language", "Live calls", "Speaking and captions for your next call", a.languages.Voice, func(code string) {
				prefs := a.languages
				prefs.Voice = code
				a.saveLanguages(prefs)
			})
		})
		if a.online && a.serverProtocol != 0 && a.serverProtocol < api.WorkbenchProtocol {
			ui.Text(c, "Update the Buddy server to change language preferences.").TextColor(c.Theme().TextMuted)
		}
		controlSection(c, "About", "", nil)
		connectionSection(c, func() {
			ui.Row(c).FillWidth().Padding(12).Gap(12).AlignItems(ui.Center).Children(func() {
				connectionDetail(c, buddyMark, "Buddy desktop", buildChannel()+" · Build "+buildNumber)
				ui.Text(c.Key("full-build-version"), buildVersion).MinWidth(0).FontSize(12).
					TextColor(c.Theme().TextMuted).Selectable()
			})
		})
	})
}

func (a *desktop) themeChoice(c *ui.Context, label, value string) {
	selected := value == a.theme || a.theme == "" && value == "light"
	btn := ui.ButtonBase(c.Key("theme-"+value)).Label(label).Selected(selected).Height(28).
		Gap(6).Padding(0, 8).Radius(5).OnClick(func() {
		if err := localconfig.SaveDesktopTheme(a.themeFile, value); err != nil {
			a.preferencesErr = "Couldn't save the desktop theme."
			return
		}
		a.theme, a.preferencesErr = value, ""
	})
	text := c.Theme().TextMuted
	if selected || btn.Hovered() {
		btn.Background(c.Theme().SurfacePressed)
		text = c.Theme().Text
	}
	btn.Children(func() {
		theme := themeFor(value)
		ui.Box(c).Size(13, 10).Shrink(0).Radius(2).Border(1, c.Theme().TextMuted).Background(theme.Background)
		ui.Text(c, label).FontSize(12).FontWeight(620).TextColor(text)
	})
}

func (a *desktop) languageSetting(c *ui.Context, key, title, description, value string, choose func(string)) {
	ui.Row(c).FillWidth().Padding(12).Gap(12).AlignItems(ui.Center).Children(func() {
		mark := buddyMark
		if key == "voice-language" {
			mark = callMark
		}
		connectionDetail(c, mark, title, description)
		name := tasks.LanguageName(value)
		anchor := ui.Button(c.Key(key), name+" ▾").Label("Preferred language for " + title).Description(name)
		picker := ui.Local(anchor, "picker", func() languagePicker { return languagePicker{} })
		anchor.Disabled(!a.online || !a.loadedLanguages || a.loadingLanguages || a.savingLanguages || a.serverProtocol < api.WorkbenchProtocol).
			OnClick(func() {
				picker.open, picker.search = !picker.open, ""
			})
		ui.PopoverBase(c, anchor, &picker.open, func(panel ui.Element) {
			panel.Width(218).Padding(7).Radius(10).Background(c.Theme().Surface).Border(1, c.Theme().Border).
				AttachTo(anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(4, 0, 0, 0).
				Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, .18))
			ui.Column(c).Gap(6).FillWidth().Children(func() {
				ui.TextInput(c.Key(key+"-search"), &picker.search).Label("Find a language").Placeholder("Find a language…")
				ui.Scroll(c.Key(key + "-options")).Height(217).FillWidth().Gap(2).Children(func() {
					filter := strings.ToLower(strings.TrimSpace(picker.search))
					found := false
					for _, code := range languageCodes {
						label := tasks.LanguageName(code)
						if !strings.Contains(strings.ToLower(label), filter) && code != filter {
							continue
						}
						found = true
						ui.Button(c.Key("language-"+code), label).Selected(code == value).FillWidth().Justify(ui.Start).
							OnClick(func() {
								picker.open = false
								if code != value {
									choose(code)
								}
							})
					}
					if !found {
						ui.Text(c, "No matching languages.").Padding(7).TextColor(c.Theme().TextMuted)
					}
				})
			})
		})
	})
}
