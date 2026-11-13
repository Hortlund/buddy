package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (a *desktop) importConnection() {
	if a.hostBusy || a.hostPending || a.window == nil {
		return
	}
	a.hostBusy, a.hostNotice = true, ""
	win, config, onboarding := a.window, a.hostFile, a.onboarding
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: "Connect to a Buddy server", ButtonLabel: "Verify access",
			Message: "Choose the private owner access file exported by the server, usually named owner.json.",
		})
		if err == nil && len(paths) != 0 {
			if onboarding {
				err = recordSetup(filepath.Dir(config), "desktop-setup-started")
			}
			if err == nil {
				err = a.saveConnection(paths[0], config)
			}
		}
		win.Update(func() {
			a.hostBusy = false
			if err != nil {
				a.hostNotice = errorMessage(err, err.Error())
			} else if len(paths) != 0 {
				a.hasSavedHost = true
				a.hostNotice, a.hostPending = "Reopen Buddy to connect to the selected server.", true
			}
		})
	}()
}

func (a *desktop) saveConnection(source, destination string) error {
	connection, err := localconfig.ReadConnection(source)
	if err != nil {
		return err
	}
	if connection.DeviceID != "" {
		return errors.New("Choose an owner connection file. Worker files only run assigned jobs.")
	}
	// A hosted access file can change the route to an existing home. The
	// source shutdown check belongs to the explicit move workflow.
	if !a.remoteHost && a.client != nil && !sameServerAddress(a.endpoint, connection.Server) {
		current, err := a.client.OwnerProfile(a.endpoint)
		if err == nil && current.Certificate == connection.Certificate {
			if err := requireLocalRetirement(destination); err != nil {
				return err
			}
		}
	}
	return a.verifyAndSaveHost(connection, destination)
}

func (a *desktop) verifyAndSaveHost(connection localconfig.Connection, destination string) error {
	client := api.NewConnectionClient(connection)
	defer client.Close()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	if _, err := client.Directory(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	return localconfig.SaveConnection(destination, connection, true)
}

func (a *desktop) useLocalHost() {
	if a.localServer != nil {
		a.startLocalHome(true, false)
		return
	}
	retired, err := localconfig.HomeRetired(filepath.Dir(a.hostFile))
	if err != nil || retired {
		a.hostNotice = "This Mac's Buddy home was retired. Stop the destination before rolling back with buddyd -rollback-move."
		return
	}
	err = os.Remove(a.hostFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		a.hostNotice = "Couldn't remove the saved server connection."
		return
	}
	a.hasSavedHost = false
	a.hostNotice, a.hostPending = "Open Buddy with the local launcher to start or reconnect to this Mac.", true
}

func (a *desktop) connectMovedHome() {
	if a.hostBusy || a.hostPending || a.window == nil {
		return
	}
	win, address, destination := a.window, strings.TrimSpace(a.moveAddress), a.hostFile
	if err := localconfig.ValidateServerURL(address); err != nil {
		a.moveNotice = err.Error()
		return
	}
	if sameServerAddress(a.endpoint, address) {
		a.moveNotice = "Enter the destination's new HTTPS address."
		return
	}
	a.hostBusy, a.moveNotice = true, "Verifying the restored server…"
	go func() {
		err := a.checkMoveSource(destination)
		if err == nil {
			var profile localconfig.Connection
			profile, err = a.client.OwnerProfile(address)
			if err == nil {
				err = a.verifyAndSaveHost(profile, destination)
			}
			if err != nil {
				err = errors.New("Couldn't verify the destination with this home's existing sign-in. Check its address and that the restored server is running.")
			}
		}
		win.Update(func() {
			a.hostBusy, a.moveNotice = false, ""
			if err != nil {
				a.moveNotice = err.Error()
				return
			}
			a.hasSavedHost, a.hostPending = true, true
			a.movedTo = address
			a.hostNotice = "Restored home verified. Reopen Buddy to switch."
		})
	}()
}

func (a *desktop) checkMoveSource(destination string) error {
	if !a.remoteHost {
		return requireLocalRetirement(destination)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	if _, err := a.client.DesktopProtocol(ctx); err == nil {
		return errors.New("This home still responds at the old address. Stop it and export with -retire-source before switching.")
	}
	return nil
}

func requireLocalRetirement(profilePath string) error {
	retired, err := localconfig.HomeRetired(filepath.Dir(profilePath))
	if err != nil {
		return err
	}
	if !retired {
		return errors.New("Export a move backup with -retire-source before connecting to a different host.")
	}
	return nil
}

func sameServerAddress(current, destination string) bool {
	old, oldErr := url.Parse(current)
	next, nextErr := url.Parse(destination)
	if oldErr != nil || nextErr != nil || old.Scheme != next.Scheme || serverPort(old) != serverPort(next) {
		return false
	}
	return strings.EqualFold(old.Hostname(), next.Hostname()) || (loopbackHost(old.Hostname()) && loopbackHost(next.Hostname()))
}

func serverPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

func sectionLabel(c *ui.Context, title string) {
	ui.Text(c, title).FontSize(13).FontWeight(680).TextColor(c.Theme().Text)
}

func connectionSection(c *ui.Context, body func()) {
	ui.Column(c).FillWidth().Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(14).Gap(1).Dividers(1, c.Theme().Border).Children(body)
}

func connectionDetail(c *ui.Context, mark *ui.SVG, title, subtitle string) {
	ui.Box(c).Size(34, 34).Shrink(0).Radius(9).Background(c.Theme().Accent.Alpha(.10)).Center().Children(func() {
		ui.Icon(c, mark).Size(16, 16).TextColor(c.Theme().Accent)
	})
	ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
		ui.Text(c, title).FontSize(14).FontWeight(680).SingleLine()
		if subtitle != "" {
			ui.Text(c, subtitle).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(subtitle)
		}
	})
}

var hostMark = svg(`<rect x="3.5" y="4.5" width="17" height="6" rx="2"/><rect x="3.5" y="13.5" width="17" height="6" rx="2"/><path d="M7 7.5h.01M7 16.5h.01" stroke-width="2.5"/>`)
var deviceMark = svg(`<rect x="3.5" y="4.5" width="17" height="12" rx="2"/><path d="M8 20h8m-4-3.5V20"/>`)

func (a *desktop) hosting(c *ui.Context) {
	sectionLabel(c, "Buddy home")
	connectionSection(c, func() {
		ui.Row(c).Padding(11, 13).Gap(9).Children(func() {
			name, status := "This Mac", "Offline"
			if a.remoteHost {
				name = "Connected server"
			}
			if a.online {
				status = "Connected"
			} else if a.localHomeRetired() && !a.remoteHost {
				status = "Moved"
			}
			connectionDetail(c, hostMark, name, a.endpoint)
			connectionStatus(c, status, a.online)
		})
		ui.Column(c).Padding(10, 13).Gap(8).Children(func() {
			if a.hostPending {
				ui.Text(c, a.hostNotice).FontSize(12).TextColor(c.Theme().TextMuted)
				a.applyHostButton(c)
				if a.movedTo != "" {
					ui.Text(c, "Restart workers with their existing setup files and the new server address.").FontSize(12).TextColor(c.Theme().TextMuted)
					ui.Button(c.Key("copy-worker-reconnect"), "Copy worker address option").Height(28).
						OnClick(func() { mygo.Clipboard.WriteText("-server " + shQuote(a.movedTo)) })
				}
				return
			}
			ui.Row(c).Gap(8).Children(func() {
				if !a.localHomeRetired() {
					if a.hasSavedHost || a.remoteHost {
						ui.Button(c.Key("local-host"), "Use local server").Height(29).
							Disabled(a.hostBusy || a.hostFile == "").OnClick(a.useLocalHost)
					} else if a.online && a.localServer != nil && !a.protocolChecked.IsZero() && a.serverProtocol < api.DesktopProtocol && a.localServer.installed() {
						ui.Button(c.Key("update-local-host"), "Update local server").Height(29).
							Disabled(a.hostBusy).OnClick(func() { a.startLocalHome(false, true) })
					} else if !a.online && a.localServer != nil {
						ui.PrimaryButton(c.Key("restart-local-host"), "Start local server").Height(29).
							Disabled(a.hostBusy).OnClick(func() { a.startLocalHome(false, false) })
					}
				}
				ui.Button(c.Key("import-host"), "Connect to server…").Height(29).
					Disabled(a.hostBusy || a.hostFile == "").OnClick(a.importConnection)
				ui.Button(c.Key("move-host"), toggleGuideLabel(a.moveOpen, "Move this Buddy home")).Height(29).
					Disabled(a.hostBusy || a.hostFile == "").OnClick(func() {
					a.moveOpen, a.hostGuideOpen = !a.moveOpen, false
				})
			})
			if a.hostNotice != "" {
				ui.Text(c, a.hostNotice).FontSize(12).TextColor(c.Theme().TextMuted)
			}
			if !a.moveOpen {
				ui.Row(c).Children(func() {
					quietControl(c.Key("host-guide"), toggleGuideLabel(a.hostGuideOpen, "Hosted server setup"),
						linkMark, true, func() { a.hostGuideOpen = !a.hostGuideOpen })
				})
				if a.hostGuideOpen {
					a.newHostGuide(c)
				}
			}
		})
		if a.moveOpen && !a.hostPending {
			a.moveGuide(c)
		}
	})
	a.workerDevices(c)
}

func connectionStatus(c *ui.Context, label string, ready bool) {
	fg := c.Theme().TextMuted
	if ready {
		fg = c.Theme().Success
	}
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		if ready {
			ui.Box(c).Size(5, 5).Radius(3).Background(fg)
		}
		ui.Text(c, label).SingleLine().FontSize(12).TextColor(fg)
	})
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
