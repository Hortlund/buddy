package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

func Prepare(ctx context.Context, req Request) (Request, error) {
	bad := func(message string) (Request, error) { return Request{}, &InputError{message} }
	if req.Kind == "" {
		req.Kind = RepoCheck
	}
	if req.Kind != RepoCheck {
		return bad("Unsupported task type.")
	}
	if !filepath.IsAbs(req.Repository) || len(req.Repository) > 4096 {
		return bad("Choose a Git repository using an absolute folder path.")
	}
	repo, err := filepath.EvalSymlinks(req.Repository)
	if err != nil {
		return bad("That repository folder is unavailable on the server.")
	}
	info, err := os.Stat(repo)
	if err != nil || !info.IsDir() {
		return bad("Choose a folder that contains a Git checkout.")
	}
	if req.Command == nil {
		req.Command = []string{"go", "test", "./..."}
	}
	if len(req.Command) > 64 || (len(req.Command) != 0 && strings.TrimSpace(req.Command[0]) == "") {
		return bad("Enter a valid check command.")
	}
	for _, arg := range req.Command {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return bad("The check command contains an invalid argument.")
		}
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 600
	}
	if req.TimeoutSeconds < 1 || req.TimeoutSeconds > 3600 {
		return bad("The timeout must be between 1 and 3600 seconds.")
	}

	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := command(checkCtx, repo, []string{"git", "rev-parse", "--show-toplevel"})
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return bad("Git is not available on the server.")
		}
		return bad("That folder is not inside an accessible Git checkout.")
	}
	req.Repository = filepath.Clean(strings.TrimSuffix(string(b), "\n"))
	return req, nil
}

// CommandLabel displays the argument vector without implying shell expansion.
func CommandLabel(args []string) string {
	if len(args) == 0 {
		return "Git status only"
	}
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = arg
		if arg == "" || strings.ContainsAny(arg, " \t\r\n\"'") {
			out[i] = strconv.Quote(arg)
		}
	}
	return strings.Join(out, " ")
}

type Emit func(stream, text string) error
type Runner func(context.Context, Task, Emit) error

func RunRepositoryCheck(ctx context.Context, t Task, emit Emit) error {
	log := &output{emit: emit, remaining: 1024 * 1024}
	git := []string{"git", "-c", "core.fsmonitor=false", "status", "--short", "--branch"}
	if err := runCommand(ctx, t.Repository, git, log); err != nil {
		return err
	}
	if len(t.Command) != 0 {
		return runCommand(ctx, t.Repository, t.Command, log)
	}
	return nil
}

func runCommand(ctx context.Context, dir string, args []string, log *output) error {
	if err := log.emit("system", "\n$ "+CommandLabel(args)+"\n"); err != nil {
		return err
	}
	cmd := command(ctx, dir, args)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	if args[0] == "git" {
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	}
	cmd.Stdout, cmd.Stderr = log.writer("stdout"), log.writer("stderr")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w", filepath.Base(args[0]), err)
	}
	return nil
}

func command(ctx context.Context, dir string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = slices.DeleteFunc(cmd.Environ(), func(v string) bool {
		name, _, _ := strings.Cut(v, "=")
		return codexCredentialEnv(name)
	})
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	return cmd
}

func codexCredentialEnv(name string) bool {
	return name == "OPENAI_API_KEY" || name == "CODEX_API_KEY"
}

type output struct {
	mu        sync.Mutex
	emit      Emit
	remaining int
	truncated bool
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func (o *output) writer(stream string) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		n := min(len(p), o.remaining)
		for chunk := range slices.Chunk(p[:n], 8192) {
			if err := o.emit(stream, string(chunk)); err != nil {
				return 0, err
			}
		}
		o.remaining -= n
		if n < len(p) && !o.truncated {
			o.truncated = true
			if err := o.emit("system", "\n[Output capped at 1 MiB; command is still running.]\n"); err != nil {
				return n, err
			}
		}
		return len(p), nil
	})
}
