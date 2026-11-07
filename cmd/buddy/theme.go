package main

import "github.com/egoist/mygo/ui"

var lightBuddyTheme = newBuddyTheme(false)
var darkBuddyTheme = newBuddyTheme(true)

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
		t.Background, t.Surface, t.Border = ui.Hex("#0D1117"), ui.Hex("#151B23"), ui.Hex("#30363D")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#161B22"), ui.Hex("#212830")
		t.Text, t.TextMuted = ui.Hex("#F0F6FC"), ui.Hex("#9198A1")
		t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = ui.Hex("#4493F8"), ui.Hex("#79C0FF"), ui.Hex("#388BFD"), ui.Hex("#0D1117")
		t.Success, t.Danger, t.Warning = ui.Hex("#3FB950"), ui.Hex("#FF7B72"), ui.Hex("#D29922")
	} else {
		t.Background, t.Surface, t.Border = ui.Hex("#FCFCFD"), ui.Hex("#FFFFFF"), ui.Hex("#E5E5E8")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#F4F4F5"), ui.Hex("#EAEAED")
		t.Text, t.TextMuted = ui.Hex("#19191B"), ui.Hex("#68686F")
		t.Accent, t.AccentHover, t.AccentPressed = t.Text, ui.Hex("#333337"), ui.Hex("#101012")
		t.Success, t.Danger, t.Warning = ui.Hex("#276448"), ui.Hex("#9C3330"), ui.Hex("#795700")
	}
	t.Selection, t.Focus = t.Accent.Alpha(.23), t.Accent.Alpha(.66)
	t.Radius, t.FontSize = 8, 13
	return t
}

func replySurface(c *ui.Context) ui.Color {
	return c.Theme().Background.Mix(c.Theme().SurfacePressed, .55)
}
