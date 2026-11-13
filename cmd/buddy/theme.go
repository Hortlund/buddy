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
		t.Background, t.Surface, t.Border = ui.Hex("#121214"), ui.Hex("#19191D"), ui.Hex("#303036")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#1E1E23"), ui.Hex("#2A2931")
		t.Text, t.TextMuted = ui.Hex("#F3F1F5"), ui.Hex("#A4A0AC")
		t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = ui.Hex("#C0AAFF"), ui.Hex("#D3C2FF"), ui.Hex("#AE95F3"), ui.Hex("#201637")
		t.Success, t.Danger, t.Warning = ui.Hex("#83D8AE"), ui.Hex("#FF9695"), ui.Hex("#E7BF83")
	} else {
		t.Background, t.Surface, t.Border = ui.Hex("#FAF9FC"), ui.Hex("#FFFFFF"), ui.Hex("#E5E1EB")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#F3F1F7"), ui.Hex("#EBE7F2")
		t.Text, t.TextMuted = ui.Hex("#201D27"), ui.Hex("#696471")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#603DAE"), ui.Hex("#714DC2"), ui.Hex("#4E318F")
		t.Success, t.Danger, t.Warning = ui.Hex("#276448"), ui.Hex("#9C3330"), ui.Hex("#795700")
	}
	t.Selection, t.Focus = t.Accent.Alpha(.23), t.Accent.Alpha(.66)
	t.Radius, t.FontSize = 8, 13
	return t
}

func replySurface(c *ui.Context) ui.Color {
	return c.Theme().Background.Mix(c.Theme().SurfacePressed, .55)
}
