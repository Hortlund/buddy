package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"

	"golang.org/x/sys/unix"
)

// Set by the macOS bundler after signing the helper. A running desktop can
// authenticate the bytes even if another process rewrites its bundle on disk.
var bundledDaemonSHA256 string

type localServer struct {
	endpoint, tokenFile, helper string
	sourceDir, workspaceDir     string
	codexPath, shellPath        string
	launcherDir, helperSHA      string
}

func bundledLocalServer(endpoint, tokenFile string) *localServer {
	if runtime.GOOS != "darwin" || filepath.Base(tokenFile) != localconfig.TokenName || len(bundledDaemonSHA256) != sha256.Size*2 {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || localconfig.ValidateServerURL(endpoint) != nil || !loopbackHost(u.Hostname()) || u.Port() == "" {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	bundle, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(executable), "..", ".."))
	if err != nil {
		return nil
	}
	helper := filepath.Join(bundle, "Contents", "Helpers", "buddyd")
	info, err := os.Stat(helper)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return &localServer{endpoint: endpoint, tokenFile: tokenFile, helper: helper, launcherDir: bundle, helperSHA: bundledDaemonSHA256}
}

func loopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || localconfig.IsLoopback(host)
}

func (s *localServer) dataDir() string { return filepath.Dir(s.tokenFile) }

func (s *localServer) installed() bool {
	_, err := os.Lstat(filepath.Join(s.dataDir(), "runtime", "buddyd"))
	return err == nil
}

func serviceLocation(dir string) (domain, target, plist string) {
	hash := sha256.Sum256([]byte(dir))
	label := fmt.Sprintf("dev.buddy.server.%x", hash[:8])
	domain = fmt.Sprintf("gui/%d", os.Getuid())
	return domain, domain + "/" + label, filepath.Join(dir, label+".plist")
}

func launchctl(ctx context.Context, args ...string) error {
	return exec.CommandContext(ctx, "/bin/launchctl", args...).Run()
}

func (s *localServer) start(ctx context.Context, restart bool) error {
	if err := os.MkdirAll(s.dataDir(), 0700); err != nil {
		return err
	}
	dir, err := localconfig.PrivateDir(s.dataDir())
	if err != nil {
		return err
	}
	if retired, err := localconfig.HomeRetired(dir); err != nil {
		return err
	} else if retired {
		return errors.New("This Mac's Buddy home was retired for a move.")
	}
	domain, target, plist := serviceLocation(dir)
	client := api.NewClient(s.endpoint, s.tokenFile)
	defer client.Close()
	if restart {
		binary, cleanup, err := s.stageDaemon(dir)
		if err != nil {
			return err
		}
		defer cleanup()
		if err := runDaemon(ctx, binary, dir, "-stop-idle-service", target); err != nil {
			return err
		}
	} else {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		_, err := client.Directory(probe)
		cancel()
		if err == nil {
			return nil
		}
	}

	started := false
	if launchctl(ctx, "print", target) != nil {
		u, _ := url.Parse(s.endpoint)
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			host = "127.0.0.1"
		}
		addr := net.JoinHostPort(host, u.Port())
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("Buddy's local port %s is in use. Close the service using it and try again.", u.Port())
		}
		listener.Close()
		if err := s.installService(ctx, dir, plist, addr); err != nil {
			if restart {
				err = errors.Join(err, launchctl(ctx, "bootstrap", domain, plist))
			}
			return err
		}
		if err := launchctl(ctx, "bootstrap", domain, plist); err != nil {
			return fmt.Errorf("Start Buddy's background service: %w", err)
		}
		started = true
	}
	wait, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		probe, done := context.WithTimeout(wait, time.Second)
		_, err := client.Directory(probe)
		done()
		if err == nil {
			return nil
		}
		select {
		case <-ticker.C:
		case <-wait.Done():
			if started {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer cancel()
				_ = launchctl(cleanup, "bootout", target)
			}
			return fmt.Errorf("The local server didn't respond. Check %s.", filepath.Join(dir, "server.log"))
		}
	}
}

type localServicePlist struct {
	Label                string
	ProgramArguments     []string
	WorkingDirectory     string
	KeepAlive            bool
	ThrottleInterval     int
	ExitTimeOut          int
	StandardOutPath      string
	StandardErrorPath    string
	EnvironmentVariables map[string]string
}

func (s *localServer) installService(ctx context.Context, dir, plist, address string) error {
	runtimeDir := filepath.Join(dir, "runtime")
	binary := filepath.Join(runtimeDir, "buddyd")
	spec, err := s.serviceConfig(ctx, dir, plist, address, binary)
	if err != nil || spec == nil {
		return err
	}
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(spec.StandardOutPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := logFile.Close(); err != nil {
		return err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", "--", "-")
	cmd.Stdin = strings.NewReader(string(data))
	xml, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("Create Buddy's launch configuration: %w", err)
	}
	file, err := os.CreateTemp(dir, ".launchd-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(xml)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := installDaemon(s.helper, binary, s.helperSHA); err != nil {
		return err
	}
	return os.Rename(file.Name(), plist)
}

func (s *localServer) serviceConfig(ctx context.Context, dir, plist, address, binary string) (*localServicePlist, error) {
	var spec localServicePlist
	switch _, err := os.Lstat(plist); {
	case errors.Is(err, os.ErrNotExist):
		_, target, _ := serviceLocation(dir)
		logPath := filepath.Join(dir, "server.log")
		spec = localServicePlist{
			Label: filepath.Base(target), ProgramArguments: []string{binary, "-addr", address, "-data-dir", dir},
			WorkingDirectory: dir, KeepAlive: true, ThrottleInterval: 5, ExitTimeOut: 10,
			StandardOutPath: logPath, StandardErrorPath: logPath,
		}
		if s.sourceDir != "" {
			spec.ProgramArguments = append(spec.ProgramArguments, "-source-dir", s.sourceDir)
		}
		if s.workspaceDir != "" {
			spec.ProgramArguments = append(spec.ProgramArguments, "-workspaces-dir", s.workspaceDir)
		}
	case err != nil:
		return nil, err
	default:
		data, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", "--", plist).Output()
		if err != nil {
			return nil, fmt.Errorf("Read Buddy's launch configuration: %w", err)
		}
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, err
		}
		if len(spec.ProgramArguments) == 0 {
			return nil, errors.New("Buddy's launch configuration is missing the server executable.")
		}
		// The build script already protects its dist directory and owns this
		// configuration, including any custom CLI and workspace arguments.
		if spec.ProgramArguments[0] != binary {
			return nil, nil
		}
	}
	if spec.EnvironmentVariables == nil {
		spec.EnvironmentVariables = make(map[string]string)
	}
	spec.EnvironmentVariables["PATH"] = s.launchPath()
	spec.ProgramArguments = setLaunchArgument(spec.ProgramArguments, "-codex", s.codexPath)
	spec.ProgramArguments = setLaunchArgument(spec.ProgramArguments, "-launcher-dir", s.launcherDir)
	return &spec, nil
}

func setLaunchArgument(args []string, name, value string) []string {
	for i := 1; i+1 < len(args); i += 2 {
		if args[i] == name {
			if value == "" {
				return append(args[:i], args[i+2:]...)
			}
			args[i+1] = value
			return args
		}
	}
	if value != "" {
		args = append(args, name, value)
	}
	return args
}

func (s *localServer) launchPath() string {
	if s.shellPath != "" {
		return s.shellPath
	}
	const system = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	paths := []string{system}
	if inherited := os.Getenv("PATH"); inherited != "" {
		paths = append([]string{inherited}, paths...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".local", "bin"))
	}
	return strings.Join(paths, string(os.PathListSeparator))
}

func installDaemon(source, destination, expectedSHA string) error {
	expected, err := hex.DecodeString(expectedSHA)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("Buddy's bundled server checksum is missing. Rebuild or reinstall Buddy.")
	}
	input, err := os.OpenFile(source, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer input.Close()
	const maxHelperBytes = 128 << 20
	if info, err := input.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxHelperBytes {
		return errors.New("Buddy's bundled server is not a valid executable file.")
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".buddyd-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(input, maxHelperBytes+1))
	if err := errors.Join(copyErr, tmp.Chmod(0700), tmp.Close()); err != nil {
		return err
	}
	if written > maxHelperBytes || hex.EncodeToString(digest.Sum(nil)) != expectedSHA {
		return errors.New("Buddy's bundled server has changed. Rebuild or reinstall Buddy before running it.")
	}
	return os.Rename(tmp.Name(), destination)
}

func (s *localServer) stageDaemon(dir string) (string, func(), error) {
	stage, err := os.MkdirTemp(dir, ".maintenance-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	binary := filepath.Join(stage, "buddyd")
	if err := installDaemon(s.helper, binary, s.helperSHA); err != nil {
		cleanup()
		return "", nil, err
	}
	return binary, cleanup, nil
}

func runDaemon(ctx context.Context, binary, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, binary, append([]string{"-data-dir", dir}, args...)...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		if detail := strings.TrimSpace(string(output)); detail != "" {
			return errors.New(detail)
		}
		return err
	}
	return nil
}

func (s *localServer) exportMove(ctx context.Context, output string) error {
	output, err := privateMoveOutput(output)
	if err != nil {
		return err
	}
	dir, err := localconfig.PrivateDir(s.dataDir())
	if err != nil {
		return err
	}
	binary, cleanup, err := s.stageDaemon(dir)
	if err != nil {
		return err
	}
	defer cleanup()
	domain, target, plist := serviceLocation(dir)
	managed := launchctl(ctx, "print", target) == nil
	if managed {
		if err := runDaemon(ctx, binary, dir, "-stop-service-for-move", target); err != nil {
			return err
		}
	}
	err = runDaemon(ctx, binary, dir, "-backup", output, "-retire-source")
	if err == nil {
		return nil
	}
	detail := err.Error()
	if retired, stateErr := localconfig.HomeRetired(dir); managed && stateErr == nil && !retired {
		recover, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if restartErr := launchctl(recover, "bootstrap", domain, plist); restartErr != nil {
			detail += "\nThe old server is stopped. Use Start local server to bring it back."
		}
	}
	return errors.New(detail)
}
