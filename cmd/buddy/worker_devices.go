package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (a *desktop) workerDevices(c *ui.Context) {
	sectionLabel(c, "Worker devices")
	connectionSection(c, func() {
		ui.Row(c).Padding(11, 13).Gap(9).Children(func() {
			name := "No workers connected"
			if len(a.devices) > 0 {
				name = "Paired workers"
			}
			connectionDetail(c, deviceMark, name, "")
			label := "Add device"
			if a.pairOpen {
				label = "Close setup"
			} else if a.pairID != "" {
				label = "Resume setup"
			}
			ui.Button(c.Key("pair-device"), label).Height(30).Disabled(!a.online || a.hostBusy || a.hostPending).OnClick(func() {
				if !a.pairOpen && a.pairID == "" {
					a.resetPairing()
				}
				a.pairOpen = !a.pairOpen
			})
		})
		if a.pairOpen {
			a.pairGuide(c)
		}
		if a.deviceErr != "" {
			ui.Text(c, a.deviceErr).Padding(9, 13).FontSize(12).TextColor(c.Theme().Danger)
		}
		if a.deviceNotice != "" {
			ui.Text(c, a.deviceNotice).Padding(9, 13).FontSize(12).TextColor(c.Theme().TextMuted)
		}
		for _, device := range a.devices {
			a.deviceRow(c, device)
		}
	})
}

func (a *desktop) resetPairing() {
	a.pairID, a.pairName, a.pairAddress, a.pairFilename, a.pairNotice, a.pairSSH, a.pairWorkspace = "", "", "", "", "", "", ""
	a.pairSameHost, a.pairTunnel, a.pairPool, a.pairEdits = false, false, false, false
	if u, err := url.Parse(a.endpoint); err == nil && u.Hostname() != "" && !loopbackHost(u.Hostname()) {
		a.pairAddress = a.endpoint
	}
}

func (a *desktop) usesWorkerTunnel() bool {
	return !a.pairSameHost && a.pairTunnel
}

func (a *desktop) workerAddress() string {
	if a.pairSameHost {
		return a.endpoint
	}
	if a.usesWorkerTunnel() {
		u, _ := url.Parse(a.endpoint)
		return "https://127.0.0.1:" + serverPort(u)
	}
	return strings.TrimSpace(a.pairAddress)
}

func (a *desktop) validateWorkerSetup() (string, error) {
	address := a.workerAddress()
	if err := localconfig.ValidateServerURL(address); err != nil {
		return "", err
	}
	if a.usesWorkerTunnel() && !validSSHHost(strings.TrimSpace(a.pairSSH)) {
		return "", errors.New("Enter the SSH destination for the server, for example you@studio.")
	}
	if path := strings.TrimSpace(a.pairWorkspace); path != "" && !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
		return "", errors.New("Enter an absolute workspace path on the worker or leave it blank.")
	}
	return address, nil
}

func (a *desktop) pairDevice() {
	if a.hostBusy || a.hostPending || !a.online || a.window == nil {
		return
	}
	name := strings.TrimSpace(a.pairName)
	address, err := a.validateWorkerSetup()
	if err != nil {
		a.pairNotice = err.Error()
		return
	}
	a.hostBusy, a.pairNotice = true, ""
	win := a.window
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: win, Title: "Save worker setup file", DefaultPath: defaultDownload("buddy-worker.json"), ButtonLabel: "Save setup file",
			Message: "Keep this private. It lets the worker claim jobs from your Buddy home.",
		})
		var id string
		if err == nil && path != "" {
			ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
			defer cancel()
			id, err = a.client.CreateWorkerSetup(ctx, name, address, path)
		}
		win.Update(func() {
			a.hostBusy = false
			if err != nil {
				a.pairNotice = errorMessage(err, "Couldn't create the setup file. Check the server and choose a new filename.")
			} else if id != "" {
				a.pairID, a.pairFilename = id, filepath.Base(path)
				a.refreshSoon()
			}
		})
	}()
}

func validSSHHost(host string) bool {
	return host != "" && !strings.HasPrefix(host, "-") && !strings.ContainsAny(host, " \r\n\t\x00")
}

func (a *desktop) workerTunnelCommand() string {
	u, _ := url.Parse(a.endpoint)
	port := serverPort(u)
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	return "ssh -N -o ExitOnForwardFailure=yes -L " + shQuote("127.0.0.1:"+port+":"+net.JoinHostPort(host, port)) +
		" " + shQuote(strings.TrimSpace(a.pairSSH))
}

func (a *desktop) workerSetupCommand() string {
	dir := `"$HOME/.buddy-worker/"` + shQuote(a.pairID)
	file := `"$worker_dir/connection.json"`
	workspace := `"$HOME/buddy-workspace"`
	workspaceDir := " " + workspace
	if path := strings.TrimSpace(a.pairWorkspace); path != "" {
		workspaceDir = ""
		workspace = shQuote(path)
		if suffix, ok := strings.CutPrefix(path, "~/"); ok {
			workspace = `"$HOME/"` + shQuote(suffix)
		}
	}
	command := "worker_dir=" + dir + "\nmkdir -p -m 700 \"$worker_dir\"" + workspaceDir + " &&\n" +
		"(test -f " + file + " || install -m 600 " + shQuote("./"+a.pairFilename) + " " + file + ") &&\n" +
		"buddyworker -connection " + file + " -workspace " + workspace
	if a.pairPool {
		command += " -accept-pool"
	}
	if a.pairEdits {
		command += " -allow-edits"
	}
	return command
}

func (a *desktop) pairGuide(c *ui.Context) {
	ui.Column(c).Padding(12, 13).Gap(9).Children(func() {
		if a.pairID != "" {
			a.pairFinish(c)
			return
		}
		sectionLabel(c, "Step 1 of 2 · Set up the worker")
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c.Key("device-name"), &a.pairName).Label("Device name").Placeholder("Device name, e.g. Studio").
				Height(31).Grow(1).MinWidth(0).Disabled(a.hostBusy)
			ui.Checkbox(c.Key("pair-same-host"), &a.pairSameHost, "Worker runs on this Mac").Disabled(a.hostBusy)
		})
		if !a.pairSameHost {
			u, err := url.Parse(a.endpoint)
			local := err == nil && loopbackHost(u.Hostname())
			if local {
				ui.Checkbox(c.Key("pair-tunnel"), &a.pairTunnel, "Reach Buddy through an SSH tunnel").Disabled(a.hostBusy)
			}
			if a.pairTunnel && local {
				ui.TextInput(c.Key("pair-ssh"), &a.pairSSH).Label("SSH destination").Placeholder("you@buddy-mac").Height(31).FillWidth().Disabled(a.hostBusy)
				ui.Text(c, "Use the server’s SSH address. The setup instructions include the tunnel command.").
					FontSize(12).TextColor(c.Theme().TextMuted)
			} else {
				ui.TextInput(c.Key("device-server"), &a.pairAddress).Label("Server address from the worker").Placeholder("https://buddy-host:7331").
					Height(31).FillWidth().Disabled(a.hostBusy)
				ui.Text(c, "Use the address as seen from the worker. If it already uses a tunnel, enter its local forwarded address and port.").
					FontSize(12).TextColor(c.Theme().TextMuted)
			}
		}
		ui.TextInput(c.Key("pair-workspace"), &a.pairWorkspace).Label("Workspace folder on the worker").
			Placeholder("Workspace folder on worker (default ~/buddy-workspace)").Height(31).FillWidth().Disabled(a.hostBusy)
		ui.Row(c).Gap(12).Children(func() {
			ui.Checkbox(c.Key("pair-pool"), &a.pairPool, "Accept shared jobs").Disabled(a.hostBusy)
			ui.Checkbox(c.Key("pair-edits"), &a.pairEdits, "Allow file edits").Disabled(a.hostBusy)
		})
		ui.PrimaryButton(c.Key("create-worker-file"), "Save setup file…").Height(31).
			Disabled(a.hostBusy || a.hostPending || !a.online || strings.TrimSpace(a.pairName) == "").OnClick(a.pairDevice)
		if a.pairNotice != "" {
			ui.Text(c, a.pairNotice).FontSize(12).TextColor(c.Theme().Danger)
		}
	})
}

func (a *desktop) pairFinish(c *ui.Context) {
	status, ready := "Waiting for device", false
	for _, device := range a.devices {
		if device.ID == a.pairID && device.Seen {
			ready = true
			status = "Paired · Offline"
			if device.Online {
				status = "Paired · Online"
			}
			break
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		sectionLabel(c, "Step 2 of 2 · Start "+strings.TrimSpace(a.pairName))
		connectionStatus(c, status, ready)
	})
	ui.Text(c, "Install buddyworker and Codex on the worker. Open a terminal in the folder containing "+a.pairFilename+" and run the copied command.").
		FontSize(12).TextColor(c.Theme().TextMuted)
	if a.usesWorkerTunnel() {
		ui.Text(c, "Run the SSH tunnel on the worker in a separate terminal and keep it open while the worker runs.").
			FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Button(c.Key("copy-worker-tunnel"), "Copy SSH tunnel command").Height(29).
			OnClick(func() { mygo.Clipboard.WriteText(a.workerTunnelCommand()) })
	}
	ui.Row(c).Gap(8).Children(func() {
		ui.PrimaryButton(c.Key("copy-device"), "Copy worker command").Height(29).
			OnClick(func() { mygo.Clipboard.WriteText(a.workerSetupCommand()) })
		ui.Button(c.Key("pair-another"), "Add another device").Height(29).OnClick(a.resetPairing)
		if ready {
			ui.Button(c.Key("done-pairing"), "Done").Height(29).OnClick(func() {
				a.pairOpen = false
				a.resetPairing()
			})
		} else {
			ui.Button(c.Key("later-pairing"), "Finish later").Height(29).OnClick(func() { a.pairOpen = false })
		}
	})
}

func (a *desktop) deviceRow(c *ui.Context, device tasks.Device) {
	ui.Row(c).Gap(8).Height(43).Padding(0, 13).Children(func() {
		ui.Text(c, device.Name).Grow(1).MinWidth(0).SingleLine().FontSize(13).FontWeight(620)
		status, action := "Offline", "Remove"
		if device.Online {
			status = "Online"
		} else if !device.Seen {
			status, action = "Awaiting setup", "Cancel setup"
		}
		connectionStatus(c, status, device.Online)
		if device.Pool {
			label := "Pool · read only"
			if device.PoolEdits {
				label = "Pool · edits allowed"
			}
			connectionStatus(c, label, false)
		}
		if device.ID == a.pairID && !a.pairOpen && !device.Seen {
			ui.Button(c.Key("resume-"+device.ID), "Show setup").Height(27).Disabled(a.hostBusy || a.hostPending).
				OnClick(func() { a.pairOpen = true })
		}
		ui.Button(c.Key("remove-"+device.ID), action).Height(27).Tooltip(action + " for " + device.Name).
			Disabled(!a.online || a.hostBusy || a.hostPending || a.removingDevice != "").
			OnClick(func() { a.removeDevice(device, action) })
	})
}

func (a *desktop) removeDevice(device tasks.Device, action string) {
	if a.window == nil || a.removingDevice != "" {
		return
	}
	a.removingDevice, a.deviceNotice = device.ID, ""
	win := a.window
	go func() {
		choice, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: action + " device",
			Message: action + " for " + device.Name + "?",
			Detail:  "Buddy revokes this device’s setup file, interrupts jobs assigned to it, and pauses its routines. Files on the device stay in place.",
			Buttons: []string{"Cancel", action}, DestructiveButtons: []int{1},
		})
		removed := err == nil && choice.Button == 1
		if removed {
			ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
			defer cancel()
			err = a.client.RevokeDevice(ctx, device.ID)
		}
		win.Update(func() {
			a.removingDevice = ""
			if err != nil {
				a.deviceNotice = errorMessage(err, "Couldn't remove the device.")
			} else if removed {
				a.deviceNotice = device.Name + " was removed."
				if a.pairID == device.ID {
					a.pairOpen = false
					a.resetPairing()
				}
				a.refreshSoon()
			}
		})
	}()
}
