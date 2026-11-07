package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"buddy/internal/localconfig"
)

type AutomationSetup struct {
	Provider          string   `json:"provider"`
	Command           string   `json:"command,omitempty"`
	Args              []string `json:"args,omitempty"`
	Tools             []string `json:"tools,omitempty"`
	AllowInteractions bool     `json:"allow_interactions"`
	Fingerprint       string   `json:"launch_fingerprint,omitempty"`
}

// This file is local to its host. Never send commands, paths or secrets from
// the server to a worker as executable configuration.
type ExecutionConfig struct {
	Environment string            `json:"environment"`
	Projects    []ProjectBinding  `json:"projects"`
	Automation  []AutomationSetup `json:"automation"`
}

func LoadExecutionConfig(path string) (ExecutionConfig, error) {
	cfg := ExecutionConfig{Environment: "host"}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return cfg, &InputError{"Keep execution.json private (0600), in a host-owned private directory, without symlinks."}
	}
	if _, err := localconfig.PrivateDir(filepath.Dir(path)); err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if len(data) > 64<<10 {
		return cfg, &InputError{"Execution configuration is too large."}
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&cfg)
	return cfg, err
}

func (r *CodexRuntime) ConfigureExecution(ctx context.Context, cfg ExecutionConfig) error {
	if cfg.Environment == "" {
		cfg.Environment = "host"
	}
	if !validTitle(cfg.Environment, 80) || len(cfg.Projects) > 64 || len(cfg.Automation) > 8 {
		return &InputError{"Invalid execution configuration."}
	}
	seenProjects := map[string]bool{}
	for i, b := range cfg.Projects {
		if !mcpName.MatchString(b.ProjectID) {
			return &InputError{"Choose a project ID from Buddy."}
		}
		if seenProjects[b.ProjectID] {
			return &InputError{"Configure one checkout per project on this host."}
		}
		seenProjects[b.ProjectID] = true
		path, err := cleanBindingPath(b.Path)
		if err != nil {
			return err
		}
		for _, protected := range slices.Concat(r.protected, r.readOnly, []string{r.migrationDir, r.attachmentDir, r.snapshotDir}) {
			if protected == "" {
				continue
			}
			if overlap, err := WorkspacePathsOverlap(path, protected); err != nil || overlap {
				return &InputError{"Keep project checkouts separate from Buddy's credentials and runtime files."}
			}
		}
		cfg.Projects[i].Path = path
	}
	seenProviders := map[string]bool{}
	for i, setup := range cfg.Automation {
		if seenProviders[setup.Provider] {
			return &InputError{"Configure each automation provider once."}
		}
		seenProviders[setup.Provider] = true
		if setup.Provider != "cua" && setup.Provider != "playwright" {
			return &InputError{"Choose Cua Driver or Playwright. Run a worker inside a Lume VM to use its desktop."}
		}
		if setup.Command == "" {
			if setup.Provider == "cua" {
				setup.Command = "cua-driver"
				setup.Args = []string{"mcp"}
			} else {
				setup.Command = "playwright-mcp"
				setup.Args = defaultBrowserArgs()
			}
		}
		launch, err := inspectMCPLaunch(ctx, setup.Command, setup.Args)
		if err != nil {
			return err
		}
		if setup.AllowInteractions && (setup.Fingerprint == "" || setup.Fingerprint != launch.fingerprint) {
			return &InputError{"Test and enable this automation provider again. Its installed command changed or has not been approved."}
		}
		setup.Command, setup.Args = launch.command, launch.args
		for _, tool := range setup.Tools {
			if !validMCPTool(tool) {
				return &InputError{"Invalid automation tool name."}
			}
		}
		if setup.AllowInteractions && len(setup.Tools) == 0 {
			return &InputError{"Test the provider and select the tools it may use before enabling interactions."}
		}
		cfg.Automation[i] = setup
	}
	r.executionMu.Lock()
	r.execution = cfg
	r.executionMu.Unlock()
	return nil
}

// An unavailable checkout or optional provider must not take the Buddy home
// offline. Keep its saved configuration untouched and require owner setup again.
func (r *CodexRuntime) LoadHostExecution(ctx context.Context, saved ExecutionConfig) error {
	base := ExecutionConfig{Environment: saved.Environment}
	if err := r.ConfigureExecution(ctx, base); err != nil {
		return err
	}
	for _, project := range saved.Projects {
		candidate := r.ExecutionConfig()
		candidate.Projects = append(candidate.Projects, project)
		if err := r.ConfigureExecution(ctx, candidate); err != nil {
			log.Printf("project %s unavailable on this host: %v", project.ProjectID, err)
		}
	}
	for _, provider := range saved.Automation {
		candidate := r.ExecutionConfig()
		candidate.Automation = append(candidate.Automation, provider)
		if err := r.ConfigureExecution(ctx, candidate); err != nil {
			log.Printf("automation provider %s needs setup on this host: %v", provider.Provider, err)
		}
	}
	return ctx.Err()
}

func (r *CodexRuntime) ExecutionConfig() ExecutionConfig {
	r.executionMu.RLock()
	defer r.executionMu.RUnlock()
	cfg := r.execution
	cfg.Projects = slices.Clone(cfg.Projects)
	cfg.Automation = slices.Clone(cfg.Automation)
	for i := range cfg.Automation {
		cfg.Automation[i].Args = slices.Clone(cfg.Automation[i].Args)
		cfg.Automation[i].Tools = slices.Clone(cfg.Automation[i].Tools)
	}
	if cfg.Environment == "" {
		cfg.Environment = "host"
	}
	return cfg
}

func (r *CodexRuntime) PersistExecution(ctx context.Context, cfg ExecutionConfig) error {
	previous := r.ExecutionConfig()
	if err := r.ConfigureExecution(ctx, cfg); err != nil {
		return err
	}
	data, err := marshalExecutionConfig(r.ExecutionConfig())
	if err == nil {
		err = writePrivateExecution(filepath.Join(r.Key.Dir, "execution.json"), data)
	}
	if err != nil {
		r.executionMu.Lock()
		r.execution = previous
		r.executionMu.Unlock()
	}
	return err
}

func marshalExecutionConfig(cfg ExecutionConfig) ([]byte, error) {
	return json.MarshalIndent(cfg, "", "  ")
}

func writePrivateExecution(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".execution-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), path)
}

func (r *CodexRuntime) DiscoverHost(ctx context.Context) HostProfile {
	cfg := r.ExecutionConfig()
	profile := HostProfile{OS: runtime.GOOS, Architecture: runtime.GOARCH, CPUs: runtime.NumCPU(), Environment: cfg.Environment, Projects: cfg.Projects, UpdatedAt: time.Now().UTC()}
	profile.Slots = 1
	if r.MCPStore != nil {
		profile.Slots = localBackgroundSlots
	}
	for _, name := range []string{"git", "go", "node", "python3", "docker", "xcodebuild", "codex", "lume"} {
		binary, err := exec.LookPath(name)
		cap := Capability{ID: name, Installed: err == nil, Enabled: err == nil, Ready: err == nil, Detail: "Not installed"}
		if err == nil {
			cap.Detail = "Installed"
			if name == "codex" {
				_, source, keyErr := r.Key.Read()
				status := r.RuntimeStatus(ctx, source)
				cap.Ready = keyErr == nil && (status == "signed_in" || status == "configured")
				cap.Detail = "Codex · " + strings.ReplaceAll(status, "_", " ")
			}
			if name != "lume" && name != "codex" {
				args := []string{"--version"}
				switch name {
				case "go":
					args = []string{"version"}
				case "docker":
					args = []string{"info", "--format", "{{.ServerVersion}}"}
				case "xcodebuild":
					args = []string{"-version"}
				}
				probe, cancel := context.WithTimeout(ctx, time.Second)
				data, probeErr := exec.CommandContext(probe, binary, args...).Output()
				cancel()
				cap.Ready = probeErr == nil
				if probeErr == nil {
					cap.Detail = strings.SplitN(string(data), "\n", 2)[0]
					if len(cap.Detail) > 180 {
						cap.Detail = cap.Detail[:180]
					}
				} else {
					cap.Detail = "Installed · readiness check failed"
				}
			}
		}
		profile.Capabilities = append(profile.Capabilities, cap)
	}
	for _, provider := range []string{"cua", "playwright"} {
		binary := "cua-driver"
		id := "desktop"
		if provider == "playwright" {
			binary = "playwright-mcp"
			id = "browser"
		}
		_, err := exec.LookPath(binary)
		cap := Capability{ID: id, Installed: err == nil, Detail: "Not enabled. Configure this host's automation tools."}
		for _, setup := range cfg.Automation {
			if setup.Provider != provider {
				continue
			}
			cap.Installed = true
			cap.Enabled = setup.AllowInteractions
			cap.Ready = cap.Enabled
			if provider == "cua" {
				cap.Ready = cap.Ready && cuaDesktopReady(ctx, setup.Command)
				cap.Detail = "Cua Driver · desktop session and OS permissions required"
			} else {
				cap.Ready = cap.Ready && chromiumAvailable()
				cap.Detail = "Playwright · isolated headless browser"
			}
			if cap.Enabled && !cap.Ready {
				cap.Detail += " · setup or permission check required"
			}
		}
		profile.Capabilities = append(profile.Capabilities, cap)
	}
	return profile
}

func cuaDesktopReady(ctx context.Context, binary string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if runtime.GOOS != "darwin" {
		data, err := exec.CommandContext(ctx, binary, "call", "list_apps").Output()
		var value any
		return err == nil && len(data) < 64<<10 && json.Unmarshal(data, &value) == nil && hasDesktopProcess(value)
	}
	data, err := exec.CommandContext(ctx, binary, "permissions", "status", "--json").Output()
	if err != nil || len(data) > 64<<10 {
		return false
	}
	var state struct {
		Accessibility bool `json:"accessibility"`
		Screen        bool `json:"screen_recording"`
	}
	if json.Unmarshal(data, &state) != nil {
		return false
	}
	return state.Accessibility && state.Screen
}

func hasDesktopProcess(value any) bool {
	switch object := value.(type) {
	case []any:
		return slices.ContainsFunc(object, hasDesktopProcess)
	case map[string]any:
		if pid, ok := object["pid"].(float64); ok && pid > 0 {
			return true
		}
		for _, child := range object {
			if hasDesktopProcess(child) {
				return true
			}
		}
	}
	return false
}

func (r *CodexRuntime) TestAutomation(ctx context.Context, provider string) (AutomationSetup, error) {
	setup := AutomationSetup{Provider: provider, AllowInteractions: true}
	switch provider {
	case "cua":
		setup.Command = "cua-driver"
		setup.Args = []string{"mcp"}
	case "playwright":
		setup.Command = "playwright-mcp"
		setup.Args = defaultBrowserArgs()
		if !chromiumAvailable() {
			return setup, &InputError{"Install Chromium for Playwright on this host before enabling browser work. See docs/execution.md."}
		}
	default:
		return setup, &InputError{"Choose Cua Driver or Playwright."}
	}
	launch, err := inspectMCPLaunch(ctx, setup.Command, setup.Args)
	if err != nil {
		return setup, err
	}
	config := MCPServerConfig{MCPServer: MCPServer{Name: "buddy-" + provider, Transport: MCPStdio, Command: launch.command, Args: launch.args, Enabled: true}, launchFingerprint: launch.fingerprint}
	test, err := r.TestMCPServer(ctx, config)
	if err != nil {
		return setup, err
	}
	setup.Command, setup.Args, setup.Tools, setup.Fingerprint = launch.command, launch.args, slices.Concat(test.Tools, test.RequiresApproval), launch.fingerprint
	if len(setup.Tools) == 0 {
		return setup, &InputError{"The provider did not expose any tools. Finish its setup on this host first."}
	}
	return setup, nil
}

func chromiumAvailable() bool {
	return chromiumExecutable() != ""
}

func defaultBrowserArgs() []string {
	args := []string{"--headless", "--isolated", "--no-webmcp"}
	if executable := chromiumExecutable(); executable != "" {
		args = append(args, "--executable-path", executable)
	}
	return args
}

func chromiumExecutable() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	home, _ := os.UserHomeDir()
	patterns := []string{
		filepath.Join(home, "Library/Caches/ms-playwright/chromium-*", "chrome-mac-*", "Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"),
		filepath.Join(home, ".cache/ms-playwright/chromium-*", "chrome-linux*", "chrome"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	}
	if cache := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); filepath.IsAbs(cache) {
		patterns = append([]string{filepath.Join(cache, "chromium-*", "chrome-*", "chrome")}, patterns...)
	}
	if cache := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(cache) {
		patterns = append(patterns, filepath.Join(cache, "ms-playwright/chromium-*", "chrome-win*", "chrome.exe"))
	}
	for _, pattern := range patterns {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			// Prefer the newest installed browser revision.
			slices.Sort(matches)
			return matches[len(matches)-1]
		}
	}
	return ""
}

func (r *CodexRuntime) executionMCP(ctx context.Context, turn Turn) ([]MCPServerConfig, []string, error) {
	var configs []MCPServerConfig
	var roots []string
	for _, setup := range r.ExecutionConfig().Automation {
		if !setup.AllowInteractions {
			continue
		}
		needed := "desktop"
		if setup.Provider == "playwright" {
			needed = "browser"
		}
		if !slices.Contains(turn.ExecutionPlan.Requirements, needed) {
			continue
		}
		launch, err := inspectMCPLaunch(ctx, setup.Command, setup.Args)
		if err != nil {
			return nil, nil, err
		}
		if launch.fingerprint != setup.Fingerprint {
			return nil, nil, &InputError{"The automation provider changed. Test and enable it again on this host."}
		}
		if job := turn.BuddyTeam.CurrentTaskID; job != "" {
			dir, err := r.frameDirectory(job)
			if err != nil {
				return nil, nil, err
			}
			exe, err := os.Executable()
			if err != nil {
				return nil, nil, err
			}
			args := append([]string{"automation-proxy", "-frames", dir, "-provider", setup.Provider, "--", launch.command}, launch.args...)
			launch, err = inspectMCPLaunch(ctx, exe, args)
			if err != nil {
				return nil, nil, err
			}
		}
		configs = append(configs, MCPServerConfig{MCPServer: MCPServer{Name: "buddy-" + setup.Provider, Transport: MCPStdio, Command: launch.command, Args: launch.args, Enabled: true, Tools: setup.Tools, ApproveTools: setup.AllowInteractions}, launchFingerprint: launch.fingerprint})
		roots = append(roots, launch.roots...)
	}
	return configs, roots, nil
}

func PrepareProjectWorkspace(ctx context.Context, workspace, job, project string, edits bool) (string, string, error) {
	if project == "" || !edits {
		return workspace, "", nil
	}
	if !mcpName.MatchString(job) || !mcpName.MatchString(project) {
		return "", "", &InputError{"Invalid project worker identity."}
	}
	var err error
	workspace, err = checkWorkspace(workspace)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return workspace, "", nil
	}
	status, err := exec.CommandContext(ctx, "git", "-C", workspace, "status", "--porcelain").Output()
	if err != nil {
		return "", "", err
	}
	if len(status) != 0 {
		return "", "", &InputError{"The project checkout has uncommitted changes. Commit or stash them before starting an isolated worker."}
	}
	base := filepath.Join(filepath.Dir(workspace), ".buddy-worktrees", project)
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(base)
	if err != nil || real != base {
		return "", "", &InputError{"The worker worktree directory changed."}
	}
	target := filepath.Join(base, job)
	branch := "buddy/" + job
	if _, statErr := os.Lstat(target); statErr == nil {
		realTarget, resolveErr := filepath.EvalSymlinks(target)
		actualBranch, branchErr := exec.CommandContext(ctx, "git", "-C", target, "symbolic-ref", "--short", "HEAD").Output()
		common, commonErr := exec.CommandContext(ctx, "git", "-C", target, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		original, originalErr := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		if resolveErr != nil || realTarget != target || branchErr != nil || strings.TrimSpace(string(actualBranch)) != branch ||
			commonErr != nil || originalErr != nil || strings.TrimSpace(string(common)) != strings.TrimSpace(string(original)) {
			return "", "", &InputError{"This worker's existing Git worktree does not match its project and branch."}
		}
		return target, branch, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", statErr
	}
	if err := exec.CommandContext(ctx, "git", "-C", workspace, "worktree", "add", "-b", branch, target, "HEAD").Run(); err != nil {
		return "", "", &InputError{"Couldn't create this worker's Git worktree. Check the project checkout and branch."}
	}
	return target, branch, nil
}
