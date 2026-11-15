package tasks

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const chatCodeDaemonGroup = "buddy-daemon"

func currentChatCodeCgroup() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" {
			return filepath.Join("/sys/fs/cgroup", path), nil
		}
	}
	return "", errors.New("a delegated cgroup v2 subtree is unavailable")
}

// PrepareChatCodeSandbox splits the delegated service cgroup before the daemon
// starts workers. The daemon and each snippet run in separate leaf cgroups.
func PrepareChatCodeSandbox() error {
	if os.Geteuid() == 0 {
		return nil
	}
	group, err := currentChatCodeCgroup()
	if err != nil {
		return nil
	}
	if filepath.Base(group) == chatCodeDaemonGroup {
		group = filepath.Dir(group)
	}
	control, err := os.OpenFile(filepath.Join(group, "cgroup.subtree_control"), os.O_WRONLY, 0)
	if err != nil {
		return nil // A service without delegation can use Docker.
	}
	defer control.Close()
	controllers, err := os.ReadFile(filepath.Join(group, "cgroup.controllers"))
	if err != nil {
		return err
	}
	available := strings.Fields(string(controllers))
	if !slices.Contains(available, "memory") || !slices.Contains(available, "pids") {
		return errors.New("the service cgroup needs delegated memory and pids controllers")
	}
	daemon := filepath.Join(group, chatCodeDaemonGroup)
	if err := os.Mkdir(daemon, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := writeChatCodeControl(daemon, "cgroup.procs", "0"); err != nil {
		return err
	}
	_, err = control.WriteString("+memory +pids")
	return err
}

func nativeChatCodeParent() string {
	group, err := currentChatCodeCgroup()
	if err != nil || os.Geteuid() == 0 || filepath.Base(group) != chatCodeDaemonGroup {
		return ""
	}
	parent := filepath.Dir(group)
	if _, err := os.Stat(filepath.Join(group, "cgroup.kill")); err != nil {
		return ""
	}
	controllers, err := os.ReadFile(filepath.Join(parent, "cgroup.subtree_control"))
	if err != nil {
		return ""
	}
	enabled := strings.Fields(string(controllers))
	if !slices.Contains(enabled, "memory") || !slices.Contains(enabled, "pids") {
		return ""
	}
	return parent
}

type chatCodeCgroup struct {
	*os.File
}

func newChatCodeCgroup(parent string, memory, processes int) (*chatCodeCgroup, error) {
	path := filepath.Join(parent, "buddy-snippet-"+strings.ToLower(rand.Text()))
	if err := os.Mkdir(path, 0755); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	group := &chatCodeCgroup{File: file}
	for name, value := range map[string]string{
		"memory.max": strconv.Itoa(memory), "memory.swap.max": "0", "memory.oom.group": "1",
		"pids.max": strconv.Itoa(processes),
	} {
		if err := writeChatCodeControl(path, name, value); err != nil {
			group.close()
			return nil, err
		}
	}
	// Opening the control checks access without signaling this new group.
	kill, err := os.OpenFile(filepath.Join(path, "cgroup.kill"), os.O_WRONLY, 0)
	if err != nil {
		group.close()
		return nil, err
	}
	_ = kill.Close()
	return group, nil
}

func writeChatCodeControl(group, name, value string) error {
	return os.WriteFile(filepath.Join(group, name), []byte(value), 0)
}

func (group *chatCodeCgroup) kill() error {
	return writeChatCodeControl(group.Name(), "cgroup.kill", "1")
}

func (group *chatCodeCgroup) close() {
	_ = group.kill()
	_ = group.File.Close()
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		err := os.Remove(group.Name())
		if err == nil || errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (group *chatCodeCgroup) contain(cmd *exec.Cmd) {
	// clone3 places the child in the cgroup before it can execute any code.
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(group.Fd())
	cmd.Cancel = func() error {
		if err := group.kill(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

func (group *chatCodeCgroup) outOfMemory() bool {
	events, err := os.ReadFile(filepath.Join(group.Name(), "memory.events"))
	if err != nil {
		return false
	}
	for _, event := range strings.Split(string(events), "\n") {
		if count, ok := strings.CutPrefix(event, "oom "); ok {
			return count != "0"
		}
	}
	return false
}

func nativeChatCodeRuntimes() map[string]chatCodeRuntime {
	parent := nativeChatCodeParent()
	if parent == "" {
		return nil
	}
	info, err := os.Stat("/usr/bin/bwrap")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil
	}
	runtimes := make(map[string]chatCodeRuntime)
	for language, candidate := range map[string]struct {
		program, filename string
		args              []string
	}{
		"go":     {"go", "main.go", []string{"build", "-o", "program", "main.go"}},
		"python": {"python3", "main.py", []string{"-I", "-B", "-u", "main.py"}},
	} {
		path, err := exec.LookPath(candidate.program)
		if err != nil {
			continue
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(path) || strings.ContainsAny(path, "*?[") {
			continue
		}
		root := path
		// The bundled Go toolchain keeps its standard library alongside the binary.
		install := filepath.Dir(filepath.Dir(path))
		if filepath.Base(filepath.Dir(path)) == "bin" &&
			language == "go" && filepath.Base(install) == "go" {
			root = install
		}
		runtimes[language] = chatCodeRuntime{binary: path, root: root, filename: candidate.filename, args: candidate.args, cgroup: parent}
	}
	return runtimes
}

func runNativeChatCode(ctx context.Context, sandbox string, toolchain chatCodeRuntime, source string, onOutput func(chatCodeResult)) chatCodeResult {
	result := chatCodeResult{Command: toolchain.commandLine()}
	sandbox = chatCodeSandbox(sandbox)
	if sandbox == "" || toolchain.cgroup == "" {
		result.Error = "Couldn't resolve the sandboxed runner."
		return result
	}
	// Keep the Linux work area outside the shared temporary mounts.
	scratchRoot, err := os.UserHomeDir()
	if err != nil || scratchRoot == "" {
		result.Error = "Couldn't locate a private folder for the run."
		return result
	}
	scratch, err := os.MkdirTemp(scratchRoot, "buddy-code-")
	if err != nil {
		result.Error = "Couldn't create a temporary folder for the run."
		return result
	}
	defer os.RemoveAll(scratch)
	dir, err := checkWorkspace(scratch)
	if err != nil || strings.ContainsAny(dir, "*?[") {
		result.Error = "Couldn't resolve the temporary folder for the run."
		return result
	}
	for _, name := range []string{"cache", "tmp", "config"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			result.Error = "Couldn't initialize the temporary folder for the run."
			return result
		}
	}
	if err := os.WriteFile(filepath.Join(dir, toolchain.filename), []byte(source), 0600); err != nil {
		result.Error = "Couldn't write the temporary source file."
		return result
	}
	rules := []string{`":root"="deny"`, `":minimal"="read"`, tomlString(toolchain.root) + `="read"`}
	if sandbox != toolchain.root {
		// On Linux bubblewrap re-executes the Codex binary inside the mounts.
		rules = append(rules, tomlString(sandbox)+`="read"`)
	}
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "CODEX_HOME=" + filepath.Join(dir, "config"),
		"TMPDIR=" + filepath.Join(dir, "tmp"), "GOCACHE=" + filepath.Join(dir, "cache"),
		"GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "CGO_ENABLED=0",
		"GOMAXPROCS=2", "GOFLAGS=-p=2"}
	payload := append([]string{toolchain.binary}, toolchain.args...)
	compiled := toolchain.filename == "main.go"
	program := payload
	if compiled {
		program = []string{filepath.Join(dir, "program")}
	}
	capture := chatCodeCapture{command: result.Command}
	host, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		result.Error = "Couldn't isolate the snippet's processes."
		return result
	}
	run := func(args []string, building bool) error {
		access, memory, processes, fileBlocks := "read", 256<<20, 32, "0"
		stageRules := append(rules, `":slash_tmp"="deny"`, `"/sys/fs/cgroup"="deny"`)
		if building {
			access, memory, processes, fileBlocks = "write", 768<<20, 48, "65536"
		} else {
			stageRules = append(stageRules, `"/var/tmp"="deny"`, `"/dev/shm"="deny"`)
		}
		group, err := newChatCodeCgroup(toolchain.cgroup, memory, processes)
		if err != nil {
			return &InputError{"Couldn't set up the Linux code run's memory and process limits."}
		}
		defer group.close()
		filesystem := "{" + strings.Join(append(stageRules, tomlString(dir)+"="+tomlString(access)), ",") + "}"
		commandArgs := []string{sandbox, "sandbox", "-C", dir, "-P", "buddy-snippet",
			"-c", `permissions.buddy-snippet.extends=":read-only"`,
			"-c", "permissions.buddy-snippet.filesystem=" + filesystem,
			"-c", "permissions.buddy-snippet.network.enabled=false", "--", "/bin/bash", "-c", `
namespace=$(readlink /proc/self/ns/pid) || exit 125
if [ "$namespace" = "$1" ]; then
  echo "The sandbox did not provide a private process namespace." >&2
  exit 125
fi
ulimit -f "$2" && ulimit -c 0 || exit 125
shift 2
exec "$@"`, "buddy-snippet", host, fileBlocks}
		cmd := command(ctx, dir, append(commandArgs, args...))
		cmd.Env = env
		// Size the sandbox launcher's thread pool for its process budget.
		cmd.Env = append(cmd.Env, "TOKIO_WORKER_THREADS=2")
		group.contain(cmd)
		err = capture.run(cmd, onOutput)
		if group.outOfMemory() {
			return &InputError{"The run exceeded its memory limit."}
		}
		return err
	}
	if compiled {
		// The compiler gets a writable workspace and its own budget.
		err = run(payload, true)
	}
	if err == nil {
		err = run(program, false)
	}
	return finishChatCode(ctx, capture.snapshot(), err)
}
