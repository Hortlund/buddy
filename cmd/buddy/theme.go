package main

import "github.com/egoist/mygo/ui"

var lightBuddyTheme = newBuddyTheme(false)
var darkBuddyTheme = newBuddyTheme(true)
var lightRoutineTheme = newRoutineTheme(lightBuddyTheme, "#B84E22")
var darkRoutineTheme = newRoutineTheme(darkBuddyTheme, "#CC5C29")

func newRoutineTheme(base *ui.Theme, accent string) *ui.Theme {
	t := *base
	t.Accent = ui.Hex(accent)
	t.InactiveAccent = t.Accent
	return &t
}

func routineTheme(theme *ui.Theme) *ui.Theme {
	if theme == darkBuddyTheme {
		return darkRoutineTheme
	}
	return lightRoutineTheme
}

func themeFor(name string) *ui.Theme {
	if name == "dark" {
		return darkBuddyTheme
	}
	return lightBuddyTheme
}

func newBuddyTheme(dark bool) *ui.Theme {
	t := ui.LightTheme()
	if dark {
		t = ui.DarkTheme()
		t.Background, t.Surface, t.Border = ui.Hex("#141414"), ui.Hex("#1E1E1E"), ui.Hex("#2D2D2D")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#242424"), ui.Hex("#2C2C2C")
		t.Text, t.TextMuted = ui.Hex("#F3F1EF"), ui.Hex("#A5A3A0")
		t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = ui.Hex("#FFA269"), ui.Hex("#FFB784"), ui.Hex("#EE9056"), ui.Hex("#281509")
		t.Success, t.Danger, t.Warning = ui.Hex("#83D8AE"), ui.Hex("#FF9695"), ui.Hex("#E7BF83")
	} else {
		t.Background, t.Surface, t.Border = ui.Hex("#FAF9F7"), ui.Hex("#FFFFFF"), ui.Hex("#E5E0DB")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#F3F1EE"), ui.Hex("#ECE8E3")
		t.Text, t.TextMuted = ui.Hex("#242120"), ui.Hex("#6B625C")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#A84513"), ui.Hex("#B74E19"), ui.Hex("#89350E")
		t.Success, t.Danger, t.Warning = ui.Hex("#276448"), ui.Hex("#9C3330"), ui.Hex("#795700")
	}
	t.Selection, t.Focus = t.Accent.Alpha(.23), t.Accent.Alpha(.66)
	t.Radius, t.FontSize = 8, 13
	return t
}

func replySurface(c *ui.Context) ui.Color {
	return c.Theme().Background.Mix(c.Theme().SurfacePressed, .55)
}
