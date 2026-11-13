package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"

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
	t := c.Theme()
	ui.Column(c).Fill().Gradient(t.Background, t.Background.Mix(t.Accent, .06), 145).Children(func() {
		ui.Box(c).Height(max(44, bar.Height)).FillWidth().Shrink(0).DragWindow()
		ui.Scroll(c.Key("setup-scroll")).Grow(1).FillWidth().Padding(10, 32, 30).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(590).AlignSelf(ui.Center).Gap(19).Children(func() {
				setupMascot(c.Key("setup-mascot"))
				step, title := 1, "Welcome to Buddy"
				if a.online || a.hostPending {
					step, title = 2, "Connect Codex"
				}
				if a.hostPending {
					title = "Server verified"
				}
				ui.Column(c.Key(step)).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, title).Role(ui.RoleHeading).FontSize(40).FontWeight(730).LetterSpacing(-1.8)
				})
				ui.Row(c).AlignSelf(ui.Center).Gap(12).Children(func() {
					for i, label := range []string{"Buddy home", "Codex"} {
						color := t.TextMuted
						if i+1 == step {
							color = t.Accent
						}
						if i != 0 {
							ui.Icon(c, arrowMark).Size(11, 11).TextColor(t.TextMuted)
						}
						ui.Text(c.Key(i), label).FontSize(12).FontWeight(620).TextColor(color)
					}
				})
				panel := ui.Column(c.Key("setup-step-" + title)).FillWidth().Gap(12)
				revealPanel(c, panel)
				panel.Children(func() {
					switch {
					case a.hostPending:
						a.applyHostButton(c)
					case a.online:
						a.setupRuntime(c)
					default:
						connectionSection(c, func() {
							label := "Start on this Mac"
							if a.hasSavedHost {
								label = "Use this Mac instead"
							}
							setupChoice(c.Key("setup-local"), label, deviceMark, a.hostBusy || a.localServer == nil,
								func() { a.startLocalHome(a.hasSavedHost, false) })
							setupChoice(c.Key("setup-remote"), "Connect to an existing server…", hostMark, a.hostBusy, a.importConnection)
						})
						if a.localServer == nil {
							ui.Text(c, "To start a local server, open the bundled Buddy app or run buddyd separately.").FontSize(12).TextColor(c.Theme().TextMuted)
						}
						quietControl(c.Key("setup-host-guide"), toggleGuideLabel(a.hostGuideOpen, "Set up a server on another machine"),
							linkMark, true, func() { a.hostGuideOpen = !a.hostGuideOpen })
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
	})
}

func setupChoice(c *ui.Context, label string, icon *ui.SVG, disabled bool, action func()) {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).FillWidth().Height(58).Padding(0, 15).
		AlignItems(ui.Center).Gap(10).Disabled(disabled).OnClick(action)
	if button.Hovered() && !disabled {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() {
		connectionDetail(c, icon, label, "")
		ui.Icon(c, arrowMark).Size(15, 15).TextColor(t.TextMuted)
	})
}

func setupMascot(c *ui.Context) {
	t := c.Theme()
	ui.Box(c).Size(116, 116).AlignSelf(ui.Center).Center().Radius(58).
		Gradient(t.Accent.Alpha(.20), t.Accent.Alpha(.04), 155).Children(func() {
		avatar(c, 87, tasks.Avatar{Color: "plum", Shape: "cloud", Finish: "satin", Animate: true}, nil, true, orbHappy)
	})
}

func (a *desktop) setupRuntime(c *ui.Context) {
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
			connectionStatus(c, label, ready)
		})
		if !ready {
			a.codexConnection(c)
		}
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
		if !ready {
			ui.Button(c.Key("setup-auth-check"), "Check Codex again").Height(32).
				Disabled(a.hostBusy || a.authBusy).OnClick(func() { a.changeAuth("read") })
		}
	})
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
