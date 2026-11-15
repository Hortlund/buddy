package main

import (
	"strconv"
	"strings"

	"buddy/internal/api"
	"buddy/internal/buildinfo"

	"github.com/egoist/mygo/ui"
)

// The desktop build can override these through the Go linker.
var buildVersion = buildinfo.Version
var buildNumber = "2"

func localServerNeedsUpdate(version string, protocol int) bool {
	if protocol > api.DesktopProtocol {
		return false
	}
	// Development builds keep their existing protocol-based upgrade behavior.
	if strings.Contains(buildVersion, "-") {
		return protocol < api.DesktopProtocol
	}
	current, target := strings.Split(version, "."), strings.Split(buildVersion, ".")
	if len(current) != 3 || len(target) != 3 {
		return protocol < api.DesktopProtocol
	}
	comparison := 0
	for i := range current {
		old, oldErr := strconv.Atoi(current[i])
		next, nextErr := strconv.Atoi(target[i])
		if oldErr != nil || nextErr != nil || old < 0 || next < 0 {
			return protocol < api.DesktopProtocol
		}
		if comparison == 0 && old != next {
			if old < next {
				comparison = -1
			} else {
				comparison = 1
			}
		}
	}
	return comparison < 0 || comparison == 0 && protocol < api.DesktopProtocol
}

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
