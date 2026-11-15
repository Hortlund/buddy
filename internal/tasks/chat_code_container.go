package tasks

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	chatGoImage     = "docker.io/library/golang@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea"
	chatNodeImage   = "docker.io/library/node@sha256:9d266751ca3d7fdbe651c878ffd5ad0a42fed49a18c283509267c97f025b7496"
	chatRubyImage   = "docker.io/library/ruby@sha256:d7c312a48736dd291d7ce043c847f04dfde9bdf7334491ca6a545a4a184395be"
	chatPythonImage = "docker.io/library/python@sha256:3fe1a0b6c2d6b2ebb572906cdabeca75aeb558c0c17db5ffefbcf8094871c32b"
)

func containerChatCodeRuntimes() map[string]chatCodeRuntime {
	result := make(map[string]chatCodeRuntime)
	docker, err := exec.LookPath("docker")
	if err != nil {
		return result
	}
	endpoint := "/var/run/docker.sock"
	if info, err := os.Stat(endpoint); err != nil || info.Mode()&os.ModeSocket == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return result
		}
		endpoint = filepath.Join(home, ".docker", "run", "docker.sock")
		if info, err := os.Stat(endpoint); err != nil || info.Mode()&os.ModeSocket == 0 {
			return result
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	capabilities, err := containerChatCommand(ctx, chatCodeRuntime{binary: docker, root: endpoint}, "info",
		"--format={{.OSType}} {{.MemoryLimit}} {{.SwapLimit}} {{.PidsLimit}}").Output()
	if err != nil || strings.TrimSpace(string(capabilities)) != "linux true true true" {
		return result
	}
	available := make(map[string]bool)
	inspected := make(map[string]bool)
	for language, runtime := range map[string]chatCodeRuntime{
		"go":         {binary: docker, root: endpoint, image: chatGoImage, filename: "main.go", args: []string{"go", "build", "-o", "program", "main.go", "&&", "./program"}},
		"python":     {binary: docker, root: endpoint, image: chatPythonImage, filename: "main.py", args: []string{"python3", "-I", "-B", "-u", "-"}},
		"javascript": {binary: docker, root: endpoint, image: chatNodeImage, filename: "main.mjs", args: []string{"node", "--input-type=module", "-"}},
		"typescript": {binary: docker, root: endpoint, image: chatNodeImage, filename: "main.mts", args: []string{"node", "--experimental-strip-types", "--input-type=module-typescript", "-"}},
		"ruby":       {binary: docker, root: endpoint, image: chatRubyImage, filename: "main.rb", args: []string{"ruby", "--disable-gems", "-"}},
		"bash":       {binary: docker, root: endpoint, image: chatGoImage, filename: "main.sh", args: []string{"bash", "--noprofile", "--norc", "-s"}},
		"sql":        {binary: docker, root: endpoint, image: chatPythonImage, filename: "main.sql", args: []string{"python3", "-I", "-B", "-u", "-c", chatSQLRunner}},
	} {
		if !inspected[runtime.image] {
			available[runtime.image] = containerChatCommand(ctx, runtime, "image", "inspect", runtime.image).Run() == nil
			inspected[runtime.image] = true
		}
		if available[runtime.image] {
			result[language] = runtime
		}
	}
	return result
}

func containerChatCommand(ctx context.Context, toolchain chatCodeRuntime, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, toolchain.binary, append([]string{"--host", "unix://" + toolchain.root}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	return cmd
}

func runContainerChatCode(ctx context.Context, toolchain chatCodeRuntime, source string, onOutput func(chatCodeResult)) chatCodeResult {
	capture := chatCodeCapture{command: toolchain.commandLine()}
	var err error
	if toolchain.filename == "main.go" {
		build := []string{"--memory=768m", "--memory-swap=768m", "--ulimit=fsize=67108864:67108864",
			"--tmpfs=/scratch:rw,noexec,nosuid,nodev,size=384m,mode=1777", "--workdir=/scratch",
			"--env=TMPDIR=/scratch", "--env=GOCACHE=/scratch/cache", "--env=HOME=/scratch",
			"--env=GOTOOLCHAIN=local", "--env=CGO_ENABLED=0", "--env=GOMAXPROCS=2", "--env=GOPROXY=off"}
		var binary bytes.Buffer
		err = runChatContainerStage(ctx, toolchain, &capture, build, []string{"/bin/bash", "-ec", `cat > main.go
go build -p 2 -o program main.go
cat program`}, strings.NewReader(source), &binary, onOutput)
		if err == nil {
			// The loader owns the executable and drops to the unprivileged UID before running it.
			run := []string{"--memory=256m", "--memory-swap=256m", "--ulimit=fsize=67108864:67108864",
				"--user=0:0", "--cap-add=SETUID", "--cap-add=SETGID",
				"--tmpfs=/scratch:rw,exec,nosuid,nodev,size=64m,mode=0755", "--workdir=/scratch", "--env=GOMAXPROCS=2"}
			err = runChatContainerStage(ctx, toolchain, &capture, run, []string{"/bin/bash", "-ec", `cat > program
chmod 555 program
exec /usr/bin/prlimit --fsize=0:0 /usr/bin/setpriv --reuid=65534 --regid=65534 --clear-groups ./program`}, &binary, nil, onOutput)
		}
	} else {
		err = runChatContainerStage(ctx, toolchain, &capture, []string{"--memory=256m", "--memory-swap=256m", "--ulimit=fsize=0:0"},
			toolchain.args, strings.NewReader(source), nil, onOutput)
	}
	return finishChatCode(ctx, capture.snapshot(), err)
}

func runChatContainerStage(ctx context.Context, toolchain chatCodeRuntime, capture *chatCodeCapture, limits, payload []string, input io.Reader, output io.Writer, onOutput func(chatCodeResult)) error {
	name := "buddy-snippet-" + strings.ToLower(rand.Text())
	args := []string{"create", "--name", name, "--pull=never", "--interactive", "--init",
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--user=65534:65534", "--pids-limit=32", "--cpus=2", "--ulimit=core=0:0", "--log-driver=none",
		"--ulimit=cpu=40:40", "--mount=type=tmpfs,destination=/dev/shm,readonly"}
	args = append(args, limits...)
	// Bound the container lifetime even if Buddy or the Docker client exits.
	args = append(args, toolchain.image, "/usr/bin/timeout", "--signal=KILL", "40s")
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = containerChatCommand(cleanup, toolchain, "rm", "--force", name).Run()
	}()
	if err := containerChatCommand(ctx, toolchain, append(args, payload...)...).Run(); err != nil {
		return &InputError{"Couldn't create the isolated code container. Check that Docker and the required image are available."}
	}
	cmd := containerChatCommand(ctx, toolchain, "start", "--attach", "--interactive", name)
	cmd.Stdin, cmd.Stdout = input, output
	cmd.WaitDelay = time.Second
	err := capture.run(cmd, onOutput)
	if err != nil && ctx.Err() == nil {
		inspect, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		status, _ := containerChatCommand(inspect, toolchain, "inspect", "--format={{.State.OOMKilled}}", name).Output()
		if strings.TrimSpace(string(status)) == "true" {
			return errors.Join(&InputError{"The run exceeded its memory limit."}, err)
		}
	}
	return err
}
