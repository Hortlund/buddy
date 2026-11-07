package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"buddy/internal/api"
	"buddy/internal/automations"
	"buddy/internal/localconfig"
	"buddy/internal/tailnet"
	"buddy/internal/tasks"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup-automation" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := tasks.RunAutomationSetup(ctx, os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "automation-proxy" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := tasks.RunAutomationProxy(ctx, os.Args[2:]); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}

	defaultDir, err := localconfig.DataDir()
	if err != nil {
		log.Fatal(err)
	}
	addr := flag.String("addr", localconfig.Address, "loopback address to listen on")
	dir := flag.String("data-dir", defaultDir, "directory for the database and local token")
	codex := flag.String("codex", "codex", "Codex CLI executable for agent turns")
	remote := flag.Bool("allow-remote", false, "allow a non-loopback listener with pinned TLS and authentication")
	connectionFile := flag.String("export-connection", "", "create an owner connection file in a private directory")
	backupFile := flag.String("backup", "", "back up a stopped Buddy server to a new private archive")
	retire := flag.Bool("retire-source", false, "retire the stopped source home when exporting a move backup")
	rollback := flag.Bool("rollback-move", false, "resume a retired source after stopping the destination server")
	restoreFile := flag.String("restore", "", "restore a Buddy backup archive into a new folder")
	restoreTo := flag.String("restore-to", "", "new folder for restored state and workspaces")
	serverURL := flag.String("server-url", "", "HTTPS origin to put in an exported connection file")
	workspaceDir := flag.String("workspaces-dir", "", "root directory for managed agent workspaces")
	runtimeDir := flag.String("runtime-dir", "", "additional private directory containing Buddy binaries")
	launcherDir := flag.String("launcher-dir", "", "desktop bundle to exclude from agent tools")
	sourceDir := flag.String("source-dir", "", "Buddy source checkout to keep read-only")
	upgradeService := flag.String("stop-idle-service", "", "launchd service to stop for an upgrade when the queue is empty")
	moveService := flag.String("stop-service-for-move", "", "stop a launchd service when no work is running, keeping queued work")
	tailnetStatus := flag.Bool("tailscale-status", false, "check whether this host is connected to Tailscale")
	tailnetSetup := flag.Bool("setup-tailscale", false, "configure private Tailscale forwarding and print its HTTPS address")
	hostInfo := flag.Bool("host-access", false, "refresh the private desktop access file for this server")
	phonePair := flag.Bool("pair-phone", false, "print a one-time iPhone pairing QR code")
	status := flag.Bool("host-status", false, "check the running home and model sign-in")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch {
	case *tailnetStatus:
		if err := tailnet.Connected(ctx); err != nil {
			log.Fatal(err)
		}
		return
	case *tailnetSetup:
		if err := setupTailnet(ctx, *addr); err != nil {
			log.Fatal(err)
		}
		return
	case *hostInfo || *phonePair:
		if err := hostAccess(ctx, *dir, *addr, *serverURL, *phonePair); err != nil {
			log.Fatal(err)
		}
		return
	case *status:
		if err := hostStatus(ctx, *dir, *addr); err != nil {
			log.Fatal(err)
		}
		return
	case *backupFile != "":
		if err := exportBackup(ctx, *dir, *backupFile, *retire); err != nil {
			log.Fatal(err)
		}
		return
	case *restoreFile != "":
		if *restoreTo == "" {
			log.Fatal("Use -restore-to with the path to a new folder.")
		}
		if err := restoreBackup(ctx, *restoreFile, *restoreTo); err != nil {
			log.Fatal(err)
		}
		return
	case *rollback:
		if err := localconfig.RollbackRetirement(*dir); err != nil {
			log.Fatal(err)
		}
		return
	case *retire:
		log.Fatal("-retire-source requires -backup")
	}
	if *connectionFile != "" {
		if err := localconfig.ExportConnection(*dir, *connectionFile, *serverURL); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *upgradeService != "" || *moveService != "" {
		target := *upgradeService
		if *moveService != "" {
			if target != "" {
				log.Fatal("Choose only one service stop option.")
			}
			target = *moveService
		}
		if err := withIdleQueue(ctx, *dir, *moveService != "", func() error {
			return stopLaunchdService(ctx, *dir, target)
		}); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := serve(ctx, *addr, *dir, *codex, *runtimeDir, *sourceDir, *workspaceDir, *remote, *launcherDir); err != nil {
		log.Fatal(err)
	}
}

func serve(ctx context.Context, addr, dir, codex, runtimeDir, sourceDir, workspaceDir string, remote bool, launcherDir string) error {
	if !remote {
		if err := localconfig.ValidateAddress(addr); err != nil {
			return err
		}
	}
	lock, err := localconfig.LockDir(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	dir = filepath.Dir(lock.Name())
	if retired, err := localconfig.HomeRetired(dir); err != nil {
		return err
	} else if retired {
		return errors.New("this Buddy home was retired for a move; connect to the destination, or stop it before using -rollback-move")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	protected := []string{dir, executable}
	config, err := localconfig.ReadStandingConfig(dir)
	if err != nil {
		return fmt.Errorf("read Buddy automation settings: %w", err)
	}
	if runtimeDir != "" {
		runtimeDir, err = localconfig.PrivateDir(runtimeDir)
		if err != nil {
			return err
		}
		protected = append(protected, runtimeDir)
	}
	if sourceDir == "" {
		// go run executes a temporary binary, but its next launch rebuilds
		// from the current checkout. Treat that source as trusted too.
		if _, err := os.Stat("cmd/buddyd/main.go"); err == nil {
			sourceDir = "."
		}
	}
	runtime, err := tasks.NewCodexRuntime(codex, sourceDir, localconfig.CodexKey{Dir: dir}, protected...)
	if err != nil {
		return fmt.Errorf("protect Buddy runtime: %w", err)
	}
	if err := runtime.ProtectLauncherDir(launcherDir); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	identity, err := localconfig.EnsureIdentity(dir)
	if err != nil {
		return fmt.Errorf("initialize server identity: %w", err)
	}
	token, err := localconfig.EnsureToken(dir)
	if err != nil {
		return fmt.Errorf("initialize token: %w", err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	runtime.MCPStore = store
	executionConfig, err := tasks.LoadExecutionConfig(filepath.Join(dir, "execution.json"))
	if err != nil {
		return err
	}
	if err := runtime.LoadHostExecution(ctx, executionConfig); err != nil {
		return err
	}
	if err := store.SaveHostProfile(ctx, "server", runtime.DiscoverHost(ctx)); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := store.SaveHostProfile(ctx, "server", runtime.DiscoverHost(ctx)); err != nil && ctx.Err() == nil {
					log.Printf("host discovery: %v", err)
				}
			}
		}
	}()

	if err := store.EnableProfiles(ctx, filepath.Join(dir, "profiles")); err != nil {
		return err
	}
	if workspaceDir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(config, 0700); err != nil {
			return err
		}
		workspaceDir, err = defaultWorkspaceRoot(ctx, store, config, dir)
		if err != nil {
			return fmt.Errorf("migrate managed workspaces: %w", err)
		}
	}
	if err := os.MkdirAll(workspaceDir, 0700); err != nil {
		return err
	}
	workspaceDir, err = localconfig.PrivateDir(workspaceDir)
	if err != nil {
		return err
	}
	if err := runtime.EnableWorkspaces(ctx, store, workspaceDir); err != nil {
		return fmt.Errorf("initialize agent workspaces: %w", err)
	}
	if err := store.Recover(ctx); err != nil {
		return fmt.Errorf("recover task queue: %w", err)
	}

	integrations := automations.New(store, config)
	if err := integrations.ConfigureMobilePush(dir); err != nil {
		log.Printf("Buddy iPhone notifications unavailable: %v", err)
	}
	runtime.Integrations = integrations
	if integrations.SetSnapshotLocation(dir, workspaceDir) {
		runtime.ProtectSnapshotDir(config.SnapshotDir)
	}
	worker := tasks.NewWorker(store, tasks.RunRepositoryCheck, func(ctx context.Context, turn tasks.Turn, emit tasks.Emit) (string, error) {
		if turn.WatchSignal != nil {
			_ = emit("work_update", "Checking watch evidence.")
			signal, err := integrations.Enrich(ctx, *turn.WatchSignal)
			if err != nil {
				return "", err
			}
			turn.WatchSignal = &signal
		}
		return runtime.Run(ctx, turn, emit)
	})
	worker.PrepareWatchStart = integrations.PrepareWatchStart
	worker.ChooseGroupBud = runtime.ChooseGroupBud
	worker.ApplyGitHubWrite = integrations.WriteGitHub
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	integrationsDone := make(chan struct{})
	go func() {
		defer close(integrationsDone)
		integrations.Run(workerCtx)
	}()
	go func() {
		defer close(workerDone)
		worker.Run(workerCtx)
	}()

	server := &http.Server{
		Handler:           api.Handler(token, store, worker, runtime, integrations),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS13},
		ReadHeaderTimeout: 4 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ServeTLS(listener, "", "") }()
	log.Printf("Buddy server is listening at https://%s", listener.Addr())

	select {
	case <-ctx.Done():
	case err = <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	stopWorker()
	shutdown, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdown); shutdownErr != nil {
		_ = server.Close()
		err = errors.Join(err, shutdownErr)
	}
	<-workerDone
	<-integrationsDone
	return err
}
