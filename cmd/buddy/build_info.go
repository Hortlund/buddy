package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// The desktop build can override these through the Go linker.
var buildVersion = "0.1.0-dev"
var buildNumber = "2"

func buildWordmark(c *ui.Context) {
	ui.Row(c).Gap(7).MinWidth(0).Children(func() {
		ui.Text(c, "Buddy").FontSize(17).FontWeight(730).LetterSpacing(-.4).Shrink(0)
		version, _, _ := strings.Cut(buildVersion, "+")
		ui.Text(c.Key("buddy-build"), version).SingleLine().MinWidth(0).FontSize(10).FontWeight(580).
			TextColor(c.Theme().TextMuted).Tooltip(buildVersion + " · Build " + buildNumber)
	})
}

func buildChannel() string {
	version, _, _ := strings.Cut(buildVersion, "+")
	_, prerelease, found := strings.Cut(version, "-")
	if !found {
		return "Stable"
	}
	channel, _, _ := strings.Cut(prerelease, ".")
	switch channel {
	case "dev":
		return "Development"
	case "nightly":
		return "Nightly"
	case "alpha":
		return "Alpha"
	case "beta":
		return "Beta"
	case "rc":
		return "Release candidate"
	default:
		return "Pre-release"
	}
}
