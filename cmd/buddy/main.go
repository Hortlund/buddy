package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log"
	"net/url"
	"os"
	"path/filepath"

	"buddy/internal/api"
	"buddy/internal/localconfig"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

//go:embed assets/Buddy.png
var appIcon []byte

func main() {
	dir, err := localconfig.DataDir()
	if err != nil {
		log.Fatal(err)
	}
	endpoint := flag.String("server", "https://"+localconfig.Address, "Buddy server URL")
	tokenPath := flag.String("token-file", filepath.Join(dir, localconfig.TokenName), "path to the server token")
	profile := flag.String("connection", "", "owner connection file")
	repo := flag.String("repo", "", "starting folder for the workspace picker")
	localSource := flag.String("local-source-dir", "", "source checkout to protect when setting up the local server")
	localWorkspaces := flag.String("local-workspaces-dir", "", "managed workspace root for the local server")
	localCodex := flag.String("local-codex", "", "Codex executable found by the local launcher")
	localPath := flag.String("local-path", "", "PATH supplied by the local launcher")
	skipSetup := flag.Bool("skip-onboarding", false, "skip the first-run welcome screen")
	flag.Parse()
	local := bundledLocalServer(*endpoint, *tokenPath)
	if local != nil {
		local.sourceDir, local.workspaceDir = *localSource, *localWorkspaces
		local.codexPath, local.shellPath = *localCodex, *localPath
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostFile := filepath.Join(filepath.Dir(*tokenPath), "desktop-connection.json")
	_, hostFileErr := os.Lstat(hostFile)
	file := *profile
	if file == "" {
		file = hostFile
	}
	connection, err := localconfig.ReadConnection(file)
	client := api.NewClient(*endpoint, *tokenPath)
	remote := false
	var hostNotice string
	if err == nil && connection.DeviceID == "" {
		client.Close()
		client, *endpoint, remote = api.NewConnectionClient(connection), connection.Server, true
	} else if *profile != "" || (!errors.Is(err, os.ErrNotExist) && err != nil) || connection.DeviceID != "" {
		hostNotice = "Couldn't load the saved server access file. Connect to another server or use the local server."
	} else if retired, _ := localconfig.HomeRetired(filepath.Dir(hostFile)); retired {
		hostNotice = "This Mac's Buddy home was retired. Use the move guide to connect to the destination."
	} else if parsed, err := url.Parse(*endpoint); err == nil {
		remote = !loopbackHost(parsed.Hostname())
	}
	defer client.Close()
	themeFile := filepath.Join(filepath.Dir(hostFile), "desktop-theme")
	app := &desktop{
		themeFile: themeFile, theme: localconfig.ReadDesktopTheme(themeFile),
		ctx: ctx, client: client, endpoint: *endpoint, initialRepo: *repo, hostFile: hostFile, canRelaunchHost: *profile == "", hasSavedHost: !errors.Is(hostFileErr, os.ErrNotExist), remoteHost: remote, hostNotice: hostNotice,
	}
	app.localServer = local
	app.integrations.bundledGoogle = bundledGoogleClient()
	app.onboarding = !*skipSetup && *profile == "" && hostNotice == "" && needsOnboarding(filepath.Dir(hostFile), remote)
	if hostNotice != "" {
		app.section = "connections"
		app.moveOpen = app.localHomeRetired()
		if app.moveOpen {
			app.moveArchive = localconfig.MoveArchivePath(filepath.Dir(hostFile))
		}
	}
	mygo.App.WhenReady(func() {
		if err := mygo.App.Dock.SetIcon(appIcon); err != nil {
			log.Printf("set Buddy app icon: %v", err)
		}
		app.window = mygo.NewWindow(mygo.WindowOptions{
			Title: "Buddy", TitleBarStyle: mygo.TitleBarHiddenInset,
			Width: 1120, Height: 750, MinWidth: 820, MinHeight: 590,
			Content: ui.View(app.view),
		})
		app.window.OnClose(func(e *mygo.CloseEvent) {
			if app.hasWorkspaceEdits() {
				e.PreventDefault()
				app.afterWorkspaceDiscard(app.window.Close)
			}
		})
		app.window.Invalidate()
	})
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if app.moveExporting {
			e.PreventDefault()
			return
		}
		if app.hasWorkspaceEdits() {
			e.PreventDefault()
			app.afterWorkspaceDiscard(mygo.App.Quit)
		}
	})
	mygo.App.OnQuit(cancel)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
