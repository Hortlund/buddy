package main

import (
	"buddy/internal/api"
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestLocalServerUpdatesNeverDowngradeANewerRelease(t *testing.T) {
	saved := buildVersion
	t.Cleanup(func() { buildVersion = saved })
	buildVersion = "0.2.10"
	for _, test := range []struct {
		server   string
		protocol int
		want     bool
	}{
		{"0.2.9", api.DesktopProtocol, true},
		{"0.2.10", api.DesktopProtocol, false},
		{"0.3.0", api.DesktopProtocol, false},
		{"0.3.0", api.DesktopProtocol - 1, false},
		{"1.0.0", api.DesktopProtocol + 1, false},
		{"", api.DesktopProtocol - 1, true},
		{"", api.DesktopProtocol, false},
	} {
		if got := localServerNeedsUpdate(test.server, test.protocol); got != test.want {
			t.Errorf("server=%q protocol=%d: got %t, want %t", test.server, test.protocol, got, test.want)
		}
	}
}

func TestMyGoSettingsShowTheHostedServerVersionAndUpdateAction(t *testing.T) {
	a := &desktop{remoteHost: true, endpoint: "https://home.example:7331", serverVersion: "0.2.0"}
	view := ui.NewTester(a.preferences, 820, 900)
	if !view.HasText("0.2.0 · https://home.example:7331") {
		t.Fatal("settings did not identify the connected server:", view.Texts())
	}
	if _, found := view.Find("Copy update command"); !found {
		t.Fatal("hosted update action is missing")
	}
	if _, found := view.Find("Update local server"); found {
		t.Fatal("a hosted connection offered to replace the local server")
	}
}
