package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// The desktop build can override these through the Go linker.
var buildVersion = "0.1.0-dev"
var buildNumber = "2"

func buildWordmark(c *ui.Context) {
	ui.Text(c, "Buddy").FontSize(16).FontWeight(700).LetterSpacing(-.35).SingleLine()
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
