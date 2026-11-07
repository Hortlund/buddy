package main

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func TestImportConnectionCreatesPrivateSettingsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := localconfig.EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	token, err := localconfig.EnsureToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"service":"buddy","desktop_protocol":38}`))
			return
		}
		if r.URL.Path != "/v1/workspace" || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("{}"))
	})
	start := func() *httptest.Server {
		server := httptest.NewUnstartedServer(handler)
		server.TLS = &tls.Config{Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS13}
		server.StartTLS()
		t.Cleanup(server.Close)
		return server
	}
	server := start()

	source := filepath.Join(dir, "owner.json")
	if err := localconfig.ExportConnection(dir, source, server.URL); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "settings", "Buddy")
	destination := filepath.Join(parent, "desktop-connection.json")
	a := &desktop{ctx: t.Context()}
	if err := a.saveConnection(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := localconfig.PrivateDir(parent); err != nil {
		t.Fatal(err)
	}
	saved, err := localconfig.ReadConnection(destination)
	if err != nil || saved.Server != server.URL || saved.Token != token {
		t.Fatalf("connection was not saved correctly: %v", err)
	}

	// Importing a hosted access file can change the route to the same home.
	a.endpoint, a.remoteHost = server.URL, true
	a.client = api.NewConnectionClient(saved)
	t.Cleanup(a.client.Close)
	alias := start()
	aliasFile := filepath.Join(dir, "alias.json")
	if err := localconfig.ExportConnection(dir, aliasFile, alias.URL); err != nil {
		t.Fatal(err)
	}
	if err := a.saveConnection(aliasFile, destination); err != nil {
		t.Fatalf("could not import another route to the running home: %v", err)
	}
	if changed, err := localconfig.ReadConnection(destination); err != nil || changed.Server != alias.URL {
		t.Fatalf("updated address wasn't saved: %v", err)
	}
	if err := a.checkMoveSource(destination); err == nil {
		t.Fatal("the explicit move flow accepted a still-running source")
	}

}

func TestInvalidSavedConnectionCanBeCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-connection.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &desktop{hostFile: path, hasSavedHost: true}
	view := ui.NewTester(a.hosting, 600, 300)
	if _, ok := view.Find("Use local server"); !ok {
		t.Fatal("invalid saved connection hid the local fallback")
	}
	a.useLocalHost()
	if _, err := os.Stat(path); !os.IsNotExist(err) || a.hasSavedHost {
		t.Fatalf("invalid connection was not removed: %v", err)
	}
}

func TestLocalFallbackCannotUnlinkTheMovedConnection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "desktop-connection.json")
	if err := os.WriteFile(path, []byte("saved destination"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := localconfig.RetireHome(dir, ""); err != nil {
		t.Fatal(err)
	}
	a := &desktop{hostFile: path, hasSavedHost: true, remoteHost: true}
	a.useLocalHost()
	if _, err := os.Stat(path); err != nil || !a.hasSavedHost {
		t.Fatalf("the retired home could be selected as a fallback: %v", err)
	}
	if err := localconfig.RollbackRetirement(dir); err != nil {
		t.Fatal(err)
	}
	a.useLocalHost()
	if _, err := os.Stat(path); !os.IsNotExist(err) || a.hasSavedHost {
		t.Fatalf("local selection did not work after explicit rollback: %v", err)
	}
}

func TestLegacyHostedServerShowsUpgradeInsteadOfChecking(t *testing.T) {
	a := &desktop{online: true, remoteHost: true, serverProtocol: 10, protocolChecked: time.Now()}
	view := ui.NewTester(a.runtimes, 600, 300)
	if _, ok := view.Find("Update server"); !ok {
		t.Fatal("legacy hosted server did not show an upgrade state")
	}
	if a.canSendAttachments() {
		t.Fatal("the desktop enabled attachment uploads on an incompatible server")
	}
}

func TestFirstLaunchSetupResumesAndFinishes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh-home")
	a := &desktop{hostFile: filepath.Join(dir, "desktop-connection.json"), onboarding: true,
		localServer: &localServer{}, endpoint: "https://127.0.0.1:7331"}
	view := ui.NewTester(a.setupView, 850, 620)
	if !needsOnboarding(dir, false) || !view.HasText("Welcome to Buddy") {
		t.Fatal("a fresh home did not show the welcome screen")
	}
	if err := view.Click("Set up a server on another machine"); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Find("Copy connection command"); !ok {
		t.Fatal("hosted setup did not explain how to connect to the installed home")
	}
	if _, ok := view.Find("Copy install command"); !ok || !view.HasText("Linux starts Buddy at boot.") {
		t.Fatal("hosted setup did not offer the installer and explain host lifetime")
	}
	if err := recordSetup(dir, "desktop-setup-started"); err != nil {
		t.Fatal(err)
	}
	if !needsOnboarding(dir, true) {
		t.Fatal("first-run setup was lost after saving a server connection")
	}
	a.online = true
	view.Frame()
	if err := view.Click("Finish setup for now"); err != nil {
		t.Fatal(err)
	}
	if a.onboarding || needsOnboarding(dir, true) {
		t.Fatal("completed setup would reopen on the next launch")
	}

	existing := t.TempDir()
	if err := os.WriteFile(filepath.Join(existing, "buddy.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if needsOnboarding(existing, false) || needsOnboarding(t.TempDir(), true) {
		t.Fatal("existing local or hosted homes were sent back to onboarding")
	}
}

func TestDeviceSetupUsesWorkerAddressAndCheckIn(t *testing.T) {
	a := &desktop{endpoint: "https://127.0.0.1:7331", online: true}
	view := ui.NewTester(a.workerDevices, 780, 680)
	if err := view.Click("Add device"); err != nil {
		t.Fatal(err)
	}
	if a.workerAddress() != "" {
		t.Fatal("a separate device inherited the Mac's loopback address")
	}
	a.pairTunnel, a.pairSSH = true, "me@studio"
	a.pairID, a.pairName, a.pairFilename = "dev_studio", "Studio", "studio's worker.json"
	a.pairWorkspace, a.pairPool = "~/my work", true
	if a.workerAddress() != a.endpoint || !strings.Contains(a.workerTunnelCommand(), "127.0.0.1:7331:127.0.0.1:7331") {
		t.Fatal("the tunnel and setup file use different addresses")
	}
	command := a.workerSetupCommand()
	if !strings.Contains(command, "'./studio'\\''s worker.json'") ||
		!strings.Contains(command, `"$HOME/"'my work' -accept-pool`) || strings.Contains(command, "-allow-edits") {
		t.Fatalf("worker command did not preserve paths and permissions: %s", command)
	}
	a.devices = []tasks.Device{{ID: a.pairID, Name: a.pairName}}
	view.Frame()
	if !view.HasText("Waiting for device") || !view.HasText("Cancel setup") {
		t.Fatal("unchecked worker appeared paired")
	}
	a.devices[0].Online, a.devices[0].Seen = true, true
	view.Frame()
	if !view.HasText("Paired · Online") || !view.HasText("Remove") {
		t.Fatal("checked-in worker did not show its online and removal states")
	}
	if err := view.Click("Add another device"); err != nil {
		t.Fatal(err)
	}
	if a.pairID != "" || !a.pairOpen || len(a.devices) != 1 {
		t.Fatal("starting the next setup disturbed the paired device")
	}
}

func TestMoveCommandsUseSavedArchiveAndSeparateListener(t *testing.T) {
	dir := t.TempDir()
	archive := "/tmp/house's backup.tar.gz"
	if err := localconfig.RetireHome(dir, archive); err != nil {
		t.Fatal(err)
	}
	a := &desktop{remoteHost: true, moveSourceDir: "~/custom state", moveAddress: "https://buddy.example",
		moveArchive: localconfig.MoveArchivePath(dir)}
	if command := a.moveSourceCommands(); !strings.Contains(command, `"$HOME/"'custom state'`) || !strings.Contains(command, "-retire-source") {
		t.Fatalf("export command lost the source directory or retirement: %s", command)
	}
	if command := a.moveRestoreCommand(); !strings.Contains(command, `"$HOME/.buddy-migration/"'house'\''s backup.tar.gz'`) {
		t.Fatalf("restore command lost the exported filename: %s", command)
	}
	if command := moveStartCommand("[fd00::10]:8349"); !strings.Contains(command, "-allow-remote -addr '[fd00::10]:8349'") {
		t.Fatalf("start command uses the wrong port: %s", command)
	}
	if command := moveStartCommand(a.moveListen); !strings.Contains(command, "-addr '127.0.0.1:7331'") || strings.Contains(command, "-allow-remote") {
		t.Fatalf("default listener doesn't support TLS passthrough on another port: %s", command)
	}
	if !sameServerAddress("https://localhost:7331", "https://127.0.0.1:7331/") ||
		!sameServerAddress("https://buddy.example", "https://BUDDY.example:443") ||
		sameServerAddress("https://buddy.example:7331", "https://buddy.example:7332") {
		t.Fatal("server address comparison failed")
	}
}

func TestWorkerSetupAcceptsAnExistingTunnelAndIgnoresHiddenSSH(t *testing.T) {
	a := &desktop{endpoint: "https://buddy.example:7331", pairAddress: "https://127.0.0.1:17331"}
	if address, err := a.validateWorkerSetup(); err != nil || address != a.pairAddress {
		t.Fatalf("the worker's forwarded port was rejected: %q, %v", address, err)
	}
	a.endpoint, a.pairTunnel, a.pairSameHost = "https://127.0.0.1:7331", true, true
	if address, err := a.validateWorkerSetup(); err != nil || address != a.endpoint {
		t.Fatalf("same-Mac setup required hidden SSH options: %q, %v", address, err)
	}
	a.pairSameHost = false
	if _, err := a.validateWorkerSetup(); err == nil {
		t.Fatal("generated tunnel accepted a missing SSH destination")
	}
}

func TestMoveBackupMustStayInProtectedDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := localconfig.EnsureMigrationDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := privateMoveOutput(filepath.Join(home, "exposed.tar.gz")); err == nil {
		t.Fatal("accepted an archive outside the migration directory")
	}
	path := filepath.Join(dir, "custom name.tar.gz")
	if got, err := privateMoveOutput(path); err != nil || got != path {
		t.Fatalf("protected archive rejected: %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := privateMoveOutput(path); err == nil {
		t.Fatal("accepted overwriting an existing archive")
	}
}

func TestBundledDaemonIsVerifiedBeforeReplacingTheInstalledCopy(t *testing.T) {
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "helper"), filepath.Join(dir, "installed")
	signed := []byte("signed helper fixture")
	if err := os.WriteFile(source, signed, 0700); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(signed))
	if err := installDaemon(source, dest, hash); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := installDaemon(source, dest, hash); err == nil {
		t.Fatal("installed a modified helper")
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != string(signed) {
		t.Fatalf("verification failure replaced the installed daemon: %q, %v", got, err)
	}
}

func TestNativeServiceKeepsLauncherCodexAndConfiguredWorkspace(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd configuration uses plutil on macOS")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "runtime", "buddyd")
	_, _, plist := serviceLocation(dir)
	content := []byte("fixture daemon")
	helper := filepath.Join(dir, "bundled")
	if err := os.WriteFile(helper, content, 0700); err != nil {
		t.Fatal(err)
	}
	s := &localServer{helper: helper, helperSHA: fmt.Sprintf("%x", sha256.Sum256(content)), shellPath: "/opt/custom/bin:/usr/bin",
		codexPath: "/opt/custom/bin/codex", launcherDir: "/private/external dist/Buddy.app", workspaceDir: "/private/workspaces"}
	if err := s.installService(t.Context(), dir, plist, localconfig.Address); err != nil {
		t.Fatal(err)
	}
	s.workspaceDir = ""
	spec, err := s.serviceConfig(t.Context(), dir, plist, localconfig.Address, binary)
	if err != nil || spec == nil {
		t.Fatalf("could not reopen native service configuration: %v", err)
	}
	if spec.EnvironmentVariables["PATH"] != s.shellPath || !slices.Contains(spec.ProgramArguments, s.codexPath) ||
		!slices.Contains(spec.ProgramArguments, s.launcherDir) || !slices.Contains(spec.ProgramArguments, "/private/workspaces") {
		t.Fatalf("configuration lost launcher options: %+v", spec)
	}
}
