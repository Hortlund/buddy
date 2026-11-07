package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"buddy/internal/localconfig"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const moveArchiveName = "buddy-home.tar.gz"

func (a *desktop) moveGuide(c *ui.Context) {
	ui.Column(c).Padding(13).Gap(12).Children(func() {
		ui.Text(c, "Move your Buddy home").FontSize(16).FontWeight(700)
		ui.Text(c, "Your chats, Buds, routines, and managed files move together. Plan for a short pause while you transfer the home.").
			FontSize(12).TextColor(c.Theme().TextMuted)
		a.moveExportStep(c)
		a.moveRestoreStep(c)
		ui.Column(c).Gap(7).Children(func() {
			sectionLabel(c, "3 · Switch Buddy to the restored home")
			ui.Text(c, "Buddy verifies the destination with this home’s sign-in. Other desktops can import a fresh owner access file from the new server.").
				FontSize(12).TextColor(c.Theme().TextMuted)
			ui.PrimaryButton(c.Key("use-moved-address"), "Verify restored server").Height(31).
				Disabled(a.hostBusy || a.hostPending || localconfig.ValidateServerURL(strings.TrimSpace(a.moveAddress)) != nil).
				OnClick(a.connectMovedHome)
		})
		if a.moveNotice != "" {
			ui.Text(c, a.moveNotice).FontSize(12).TextColor(c.Theme().TextMuted)
		}
	})
}

func (a *desktop) moveExportStep(c *ui.Context) {
	ui.Column(c).Gap(7).Children(func() {
		sectionLabel(c, "1 · Export from the old server")
		if a.moveArchive != "" {
			ui.Text(c, "Backup saved. The old home is retired.").FontSize(12).TextColor(c.Theme().Success)
			ui.Text(c, a.moveArchive).FontSize(12).TextColor(c.Theme().TextMuted)
			return
		}
		if a.localHomeRetired() && !a.remoteHost {
			ui.Text(c, "This home is retired. Use your move backup to continue with step 2.").
				FontSize(12).TextColor(c.Theme().TextMuted)
			return
		}
		if !a.remoteHost && a.localServer != nil {
			ui.Text(c, "When running jobs finish, Buddy can stop the local service and save a private backup. Queued jobs move with it.").
				FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Button(c.Key("export-move"), "Stop and save move backup…").Height(29).
				Disabled(a.hostBusy).OnClick(a.exportLocalMove)
			return
		}
		ui.Text(c, "On the old host, let work finish and stop buddyd with its service manager. Then export a backup that retires the old home.").
			FontSize(12).TextColor(c.Theme().TextMuted)
		if a.remoteHost {
			ui.TextInput(c.Key("move-source-dir"), &a.moveSourceDir).Label("State directory on the old host").
				Placeholder("State directory on the old host (default ~/buddy-state)").Height(31).FillWidth().Disabled(a.hostBusy)
		}
		ui.Button(c.Key("copy-move"), "Copy export command").Height(29).Disabled(a.hostBusy).
			OnClick(func() { mygo.Clipboard.WriteText(a.moveSourceCommands()) })
	})
}

func (a *desktop) moveRestoreStep(c *ui.Context) {
	ui.Column(c).Gap(7).Children(func() {
		sectionLabel(c, "2 · Restore on the new server")
		ui.Text(c, "Install buddyd and Codex on the new host. Create the private transfer folder, copy your backup into it, then run the restore and start commands there.").
			FontSize(12).TextColor(c.Theme().TextMuted)
		ui.TextInput(c.Key("moved-address"), &a.moveAddress).Label("New server HTTPS address").Placeholder("https://buddy-host:7331").
			Height(31).FillWidth().Disabled(a.hostBusy)
		ui.TextInput(c.Key("move-listen-address"), &a.moveListen).Label("Daemon listen IP and port").
			Placeholder("Listen address on the new host (default 127.0.0.1:7331)").Height(31).FillWidth().Disabled(a.hostBusy)
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c.Key("copy-move-folder"), "Copy folder setup").Height(29).Disabled(a.hostBusy).
				OnClick(func() { mygo.Clipboard.WriteText("mkdir -m 700 -p \"$HOME/.buddy-migration\"\n") })
			ui.Button(c.Key("copy-restore"), "Copy restore command").Height(29).Disabled(a.hostBusy).
				OnClick(func() { mygo.Clipboard.WriteText(a.moveRestoreCommand()) })
			ui.Button(c.Key("copy-moved-start"), "Copy start command").Height(29).
				Disabled(a.hostBusy || moveListener(a.moveListen) == "").
				OnClick(func() { mygo.Clipboard.WriteText(moveStartCommand(a.moveListen)) })
		})
		ui.Text(c, "Use the default listener with a TLS passthrough proxy. For a direct connection, enter the host’s private IP and port. Set up Codex and transfer external project folders separately.").
			FontSize(12).TextColor(c.Theme().TextMuted)
	})
}

func (a *desktop) exportLocalMove() {
	if a.hostBusy || a.hostPending || a.window == nil || a.localServer == nil || a.remoteHost {
		return
	}
	a.hostBusy, a.moveNotice = true, ""
	win := a.window
	go func() {
		choice, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "Move this Buddy home",
			Message: "Stop the local server and export your home?",
			Detail:  "The app will switch over once you restore the backup on the new host. The archive contains private server and integration credentials.",
			Buttons: []string{"Cancel", "Choose backup location"}, DestructiveButtons: []int{1},
		})
		var path string
		if err == nil && choice.Button == 1 {
			var migration string
			migration, err = localconfig.EnsureMigrationDir()
			if err == nil {
				path, err = mygo.Dialog.Save(mygo.SaveDialogOptions{
					Parent: win, Title: "Save Buddy move backup", DefaultPath: filepath.Join(migration, moveArchiveName), ButtonLabel: "Stop and export",
					Message: "Choose a new filename in ~/.buddy-migration, the folder protected from Buds and workers.",
				})
			}
		}
		win.Update(func() {
			if err != nil || path == "" {
				a.hostBusy = false
				if err != nil {
					a.moveNotice = err.Error()
				}
				return
			}
			a.moveExporting, a.moveNotice = true, "Saving your move backup…"
			go func() {
				err := a.localServer.exportMove(context.WithoutCancel(a.ctx), path)
				win.Update(func() {
					a.hostBusy, a.moveExporting, a.moveNotice = false, false, ""
					if err != nil {
						a.moveNotice = err.Error()
					} else {
						a.moveArchive = path
						a.online = false
					}
					a.refreshSoon()
				})
			}()
		})
	}()
}

func (a *desktop) moveSourceCommands() string {
	path := `"$HOME/buddy-state"`
	command := "buddyd"
	if !a.remoteHost {
		path = shQuote(filepath.Dir(a.hostFile))
	} else if value := strings.TrimSpace(a.moveSourceDir); value != "" {
		if suffix, ok := strings.CutPrefix(value, "~/"); ok {
			path = `"$HOME/"` + shQuote(suffix)
		} else {
			path = shQuote(value)
		}
	}
	return "# After stopping the old server, run this on the old host.\n" +
		"umask 077\nmkdir -p \"$HOME/.buddy-migration\" &&\n" +
		command + " -data-dir " + path + " -backup \"$HOME/.buddy-migration/" + moveArchiveName + "\" -retire-source\n"
}

func (a *desktop) moveRestoreCommand() string {
	file := moveArchiveName
	if a.moveArchive != "" {
		file = filepath.Base(a.moveArchive)
	}
	return "# Transfer the move backup into $HOME/.buddy-migration first.\n" +
		"buddyd -restore \"$HOME/.buddy-migration/\"" + shQuote(file) + " -restore-to \"$HOME/buddy-home\"\n"
}

func moveListener(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return localconfig.Address
	}
	host, port, err := net.SplitHostPort(value)
	n, numberErr := strconv.Atoi(port)
	if err != nil || numberErr != nil || n < 1 || n > 65535 || net.ParseIP(host) == nil {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(n))
}

func moveStartCommand(listener string) string {
	address := moveListener(listener)
	if address == "" {
		return ""
	}
	host, _, _ := net.SplitHostPort(address)
	command := "buddyd"
	if !localconfig.IsLoopback(host) {
		command += " -allow-remote"
	}
	return command + " -addr " + shQuote(address) +
		" -data-dir \"$HOME/buddy-home/state\" -workspaces-dir \"$HOME/buddy-home/workspaces\"\n"
}

func privateMoveOutput(path string) (string, error) {
	dir, err := localconfig.EnsureMigrationDir()
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || parent != dir {
		return "", errors.New("Save the move backup in ~/.buddy-migration, which is protected from agent tools.")
	}
	path = filepath.Join(dir, filepath.Base(path))
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("Choose a new backup filename.")
	}
	return path, nil
}
