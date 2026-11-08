package tasks

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Workspace preparation runs before the model sandbox. Git may read repository
// configuration, but must not execute its hooks, helpers, or transports here.
func projectGit(ctx context.Context, workspace string, args ...string) *exec.Cmd {
	// An includeIf can activate different helpers after worktree add switches
	// to its new branch. A source-checkout snapshot cannot authorize those.
	includes, err := projectGitConfigKeys(ctx, workspace, true, `^(include\.path|includeif\..*\.path)$`)
	if err == nil && len(includes) != 0 {
		err = &InputError{"Automatic worktrees cannot prepare repository-specific Git configuration includes safely. Use a checkout without config includes, or assign the work to a Bud workspace instead of a registered project."}
	}
	if err != nil {
		cmd := projectGitCommand(ctx, workspace, args...)
		cmd.Err = err
		return cmd
	}
	// Older Git versions have per-hook switches but no per-event switch.
	// Config queries do not execute hooks; disable each configured name as well.
	keys, err := projectGitConfigKeys(ctx, workspace, false, `^hook\..*\.command$`)
	if err != nil {
		cmd := projectGitCommand(ctx, workspace, args...)
		cmd.Err = err
		return cmd
	}
	cmd := projectGitCommand(ctx, workspace, args...)
	count := 0
	for _, key := range bytes.Split(keys, []byte{0}) {
		if len(key) != 0 {
			// A valid hook name may contain '='. Keep the key and value
			// separate instead of passing an ambiguous `-c key=value`.
			cmd.Env = append(cmd.Env, "GIT_CONFIG_KEY_"+strconv.Itoa(count)+"="+strings.TrimSuffix(string(key), ".command")+".enabled",
				"GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"=false")
			count++
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
	return cmd
}

func projectGitConfigKeys(ctx context.Context, workspace string, noIncludes bool, pattern string) ([]byte, error) {
	args := []string{"config", "--null", "--name-only"}
	if noIncludes {
		args = append(args, "--no-includes")
	}
	data, err := projectGitCommand(ctx, workspace, append(args, "--get-regexp", pattern)...).Output()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return nil, nil // No matching keys.
	}
	return data, err
}

func projectGitCommand(ctx context.Context, workspace string, args ...string) *exec.Cmd {
	flags := []string{"-C", workspace,
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false",
		"-c", "core.attributesFile=" + os.DevNull, "-c", "submodule.recurse=false",
		"-c", "hook.post-checkout.enabled=false", "-c", "hook.reference-transaction.enabled=false",
		"-c", "hook.post-index-change.enabled=false", "-c", "protocol.allow=never"}
	cmd := exec.CommandContext(ctx, "git", append(flags, args...)...)
	for _, env := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(env), "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_ALLOW_PROTOCOL=")
	cmd.WaitDelay = time.Second
	return cmd
}

func checkProjectGitHelpers(ctx context.Context, workspace string) error {
	index, err := projectGit(ctx, workspace, "ls-files", "--stage", "-z").Output()
	if err != nil {
		return err
	}
	var paths bytes.Buffer
	for _, record := range bytes.Split(index, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, name, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return &InputError{"Couldn't inspect this project's Git index."}
		}
		if bytes.HasPrefix(metadata, []byte("160000 ")) {
			// Status checks initialized submodules in a separate Git process.
			if _, err := os.Lstat(filepath.Join(workspace, string(name), ".git")); !os.IsNotExist(err) {
				return &InputError{"Automatic worktrees cannot prepare initialized submodules safely. Use a checkout without initialized submodules, or assign the work to a Bud workspace instead of a registered project."}
			}
		}
		paths.Write(name)
		paths.WriteByte(0)
	}
	for _, cached := range []bool{false, true} {
		args := []string{"check-attr", "-z", "--all", "--stdin"}
		if cached {
			args = append(args, "--cached")
		}
		cmd := projectGit(ctx, workspace, args...)
		cmd.Stdin = bytes.NewReader(paths.Bytes())
		attrs, err := cmd.Output()
		if err != nil {
			return err
		}
		fields := bytes.Split(attrs, []byte{0})
		for i := 0; i+2 < len(fields); i += 3 {
			if string(fields[i+1]) == "filter" {
				// --all omits unspecified attributes. Do not whitelist textual
				// states: "unset" and "unspecified" can also be driver names.
				return &InputError{"Automatic worktrees cannot prepare files with Git filters safely (for example LFS or git-crypt). Use a checkout without filtered files, or assign the work to a Bud workspace instead of a registered project."}
			}
		}
	}
	return nil
}
