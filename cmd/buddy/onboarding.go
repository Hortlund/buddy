package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"buddy/internal/localconfig"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func needsOnboarding(dir string, hasConnection bool) bool {
	if _, err := os.Lstat(filepath.Join(dir, "desktop-setup-complete")); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if _, err := os.Lstat(filepath.Join(dir, "desktop-setup-started")); err == nil {
		return true
	}
	_, err := os.Lstat(filepath.Join(dir, "buddy.db"))
	return !hasConnection && errors.Is(err, os.ErrNotExist)
}

func recordSetup(dir, marker string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	dir, err := localconfig.PrivateDir(dir)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, marker), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

func (a *desktop) setupView(c *ui.Context) {
	bar := c.TitleBar()
	ui.Column(c).Fill().Background(c.Theme().Background).Children(func() {
		ui.Box(c).Height(max(44, bar.Height)).FillWidth().Shrink(0).DragWindow()
		ui.Scroll(c.Key("setup-scroll")).Grow(1).FillWidth().Padding(25, 32).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(570).AlignSelf(ui.Center).Gap(14).Children(func() {
				switch {
				case a.hostPending:
					pageHeading(c, "Server verified", "Reopen Buddy to finish setting up your connection.")
					a.applyHostButton(c)
				case a.online:
					a.setupRuntime(c)
				default:
					pageHeading(c, "Welcome to Buddy", "Choose where your chats, files, and background work will live.")
					sectionLabel(c, "Step 1 of 2 · Buddy server")
					connectionSection(c, func() {
						ui.Row(c).Padding(13).Gap(10).Children(func() {
							connectionDetail(c, deviceMark, "This Mac", "Work can continue after you close the app.")
							label := "Start on this Mac"
							if a.hasSavedHost {
								label = "Use this Mac instead"
							}
							ui.PrimaryButton(c.Key("setup-local"), label).Height(32).
								Disabled(a.hostBusy || a.localServer == nil).OnClick(func() { a.startLocalHome(a.hasSavedHost, false) })
						})
						ui.Row(c).Padding(13).Gap(10).Children(func() {
							connectionDetail(c, hostMark, "Another server", "Use a Buddy server that’s already running.")
							ui.Button(c.Key("setup-remote"), "Connect to server…").Height(32).
								Disabled(a.hostBusy).OnClick(a.importConnection)
						})
					})
					if a.localServer == nil {
						ui.Text(c, "To start a local server, open the bundled Buddy app or run buddyd separately.").FontSize(12).TextColor(c.Theme().TextMuted)
					}
					ui.Text(c, "For overnight work, keep this Mac awake or host Buddy on a machine that stays on.").
						FontSize(12).TextColor(c.Theme().TextMuted)
					ui.Button(c.Key("setup-host-guide"), toggleGuideLabel(a.hostGuideOpen, "Set up a server on another machine")).
						Height(29).OnClick(func() { a.hostGuideOpen = !a.hostGuideOpen })
					if a.hostGuideOpen {
						a.newHostGuide(c)
					}
				}
				if a.hostNotice != "" {
					ui.Text(c, a.hostNotice).FontSize(12).TextColor(c.Theme().TextMuted)
				}
			})
		})
	})
}

func (a *desktop) setupRuntime(c *ui.Context) {
	pageHeading(c, "Get your first Bud ready", "Your Buddy server is connected. Set up Codex so your Bud can reply and do work.")
	sectionLabel(c, "Step 2 of 2 · Codex")
	ready := a.auth.State == "configured" || a.auth.State == "signed_in"
	label := "Setup available"
	switch {
	case ready:
		label = "Ready"
	case a.auth.State == "missing":
		label = "Install required"
	case a.authBusy:
		label = "Checking"
	}
	connectionSection(c, func() {
		ui.Row(c).Padding(13).Gap(8).Children(func() {
			connectionDetail(c, runtimeMark, "Codex", "Chat and background work")
			connectionBadge(c, label, ready)
		})
		a.codexConnection(c)
	})
	if a.auth.State == "missing" && !a.remoteHost && a.localServer != nil {
		ui.Button(c.Key("setup-runtime-restart"), "Restart server after installing Codex").Height(29).
			Disabled(a.hostBusy || a.authBusy).OnClick(func() { a.startLocalHome(false, true) })
	}
	ui.Row(c).Gap(8).Children(func() {
		label := "Finish setup for now"
		if ready {
			label = "Open Buddy"
		}
		ui.PrimaryButton(c.Key("finish-setup"), label).Height(32).
			Disabled(a.hostBusy || a.authBusy).OnClick(a.finishSetup)
		ui.Button(c.Key("setup-auth-check"), "Check Codex again").Height(32).
			Disabled(a.hostBusy || a.authBusy).OnClick(func() { a.changeAuth("read") })
	})
	if !ready {
		ui.Text(c, "You can finish setting up Codex later in Connections → Runtimes.").FontSize(12).TextColor(c.Theme().TextMuted)
	}
}

func (a *desktop) finishSetup() {
	if err := recordSetup(filepath.Dir(a.hostFile), "desktop-setup-complete"); err != nil {
		a.hostNotice = "Couldn't save setup progress. " + err.Error()
		return
	}
	a.onboarding, a.section, a.hostNotice = false, "", ""
	a.authDraft = ""
	a.refreshSoon()
}

func (a *desktop) startLocalHome(switchHost, restart bool) {
	if a.localServer == nil || a.hostBusy || a.hostPending || a.window == nil || a.authBusy && restart {
		return
	}
	win, onboarding := a.window, a.onboarding
	a.hostBusy, a.hostNotice = true, "Starting the local server…"
	go func() {
		var err error
		if onboarding {
			err = recordSetup(filepath.Dir(a.hostFile), "desktop-setup-started")
		}
		if err == nil {
			err = a.localServer.start(a.ctx, restart)
		}
		if err == nil && switchHost {
			err = os.Remove(a.hostFile)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		win.Update(func() {
			a.hostBusy, a.hostNotice = false, ""
			if err != nil {
				a.hostNotice = err.Error()
			} else if switchHost {
				a.hasSavedHost, a.hostPending = false, true
				a.hostNotice = "Local server verified. Reopen Buddy to connect to it."
			} else {
				a.auth.State, a.auth.Source, a.authErr = "", "", ""
				a.protocolChecked = time.Time{}
			}
			a.refreshSoon()
		})
	}()
}

func toggleGuideLabel(open bool, label string) string {
	if open {
		return "Hide guide"
	}
	return label
}

func (a *desktop) applyHostButton(c *ui.Context) {
	if !a.canRelaunchHost {
		ui.Text(c, "Reopen without the command-line connection override to use your saved server.").FontSize(12)
		return
	}
	ui.PrimaryButton(c.Key("apply-host"), "Reopen Buddy").Height(30).
		OnClick(func() { a.afterWorkspaceDiscard(mygo.App.Relaunch) })
}

const hostInstallCommand = "curl -fsSL https://raw.githubusercontent.com/Hortlund/buddy/main/script/install-server.sh | bash\n"

func (a *desktop) newHostGuide(c *ui.Context) {
	ui.Column(c).Padding(13).Gap(8).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
		ui.Text(c, "1. Run the installer on your Linux server or always-on Mac. It installs Buddy, Codex, and Go, starts the service, and sets up Tailscale.").FontSize(12)
		ui.PrimaryButton(c.Key("copy-host-start"), "Copy install command").Height(28).OnClick(func() {
			mygo.Clipboard.WriteText(hostInstallCommand)
		})
		ui.Text(c, "2. Follow the Tailscale sign-in link. Connect this desktop and your phone to the same Tailscale account. Then run buddy-server login on the host, or add an API key here after connecting.").FontSize(12)
		ui.Text(c, "3. The installer prints a private access file and a command to copy it to this desktop. Choose that file in Connect to server. On iPhone, scan the QR from buddy-server pair.").FontSize(12)
		ui.Button(c.Key("copy-host-access"), "Copy connection command").Height(28).OnClick(func() {
			mygo.Clipboard.WriteText("buddy-server connect\n")
		})
		ui.Text(c, "Linux starts Buddy at boot. A Mac must stay awake and signed in. Rerunning setup preserves existing data. Keep the access file private; it grants owner access.").
			FontSize(11).TextColor(c.Theme().TextMuted)
	})
}

func (a *desktop) localHomeRetired() bool {
	retired, _ := localconfig.HomeRetired(filepath.Dir(a.hostFile))
	return a.hostFile != "" && retired
}
