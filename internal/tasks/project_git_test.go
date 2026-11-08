package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func projectGitFixture(t *testing.T) (string, func(...string) string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		data, err := projectGit(t.Context(), repo, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, data, err)
		}
		return string(data)
	}
	git("init", "-q")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	return repo, git
}

func TestProjectPreparationDoesNotExecuteGitHooks(t *testing.T) {
	for _, hook := range []string{"traditional", "named", "named_with_equals", "named_with_period", "fsmonitor"} {
		t.Run(hook, func(t *testing.T) {
			repo, git := projectGitFixture(t)
			marker := filepath.Join(t.TempDir(), "executed")
			t.Setenv("BUDDY_TEST_MARKER", marker)
			script := filepath.Join(repo, "helper")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf ran > \"$BUDDY_TEST_MARKER\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			git("add", "helper")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "helper")
			switch hook {
			case "traditional":
				hooks := filepath.Join(repo, ".githooks")
				if err := os.Mkdir(hooks, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(script, filepath.Join(hooks, "post-checkout")); err != nil {
					t.Fatal(err)
				}
				git("add", "-A")
				git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "tracked hook")
				git("config", "core.hooksPath", ".githooks")
			case "named", "named_with_equals", "named_with_period":
				name := "fixture"
				if hook == "named_with_equals" {
					name = "my=hook"
				} else if hook == "named_with_period" {
					name = "my.hook"
				}
				git("config", "hook."+name+".event", "post-checkout")
				git("config", "hook."+name+".command", script)
			case "fsmonitor":
				git("config", "core.fsmonitor", script)
			}
			dir, branch, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_hooks", "prj_hooks", true)
			if err != nil || dir == repo || branch == "" {
				t.Fatalf("ordinary preparation failed: %s %s %v", dir, branch, err)
			}
			if resumed, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_hooks", "prj_hooks", true); err != nil || resumed != dir {
				t.Fatal("resume failed", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("repository helper ran outside the model sandbox", err)
			}
		})
	}
}

func TestProjectPreparationRejectsFiltersBeforeExecutingThem(t *testing.T) {
	for _, source := range []string{"worktree", "index", "info", "nested", "unspecified", "unset"} {
		t.Run(source, func(t *testing.T) {
			repo, git := projectGitFixture(t)
			marker := filepath.Join(t.TempDir(), "executed")
			t.Setenv("BUDDY_TEST_MARKER", marker)
			helper := filepath.Join(t.TempDir(), "filter")
			if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf ran > \"$BUDDY_TEST_MARKER\"\ncat\n"), 0700); err != nil {
				t.Fatal(err)
			}
			name, attr, driver := "file.txt", ".gitattributes", "fixture"
			if source == "nested" {
				if err := os.Mkdir(filepath.Join(repo, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				name, attr = "nested/file.txt", "nested/.gitattributes"
			}
			if source == "unspecified" || source == "unset" {
				driver = source
			}
			if source == "info" {
				attr = ".git/info/attributes"
			}
			if err := os.WriteFile(filepath.Join(repo, name), []byte("text\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, attr), []byte("*.txt filter="+driver+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "-A")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "filtered file")
			if source == "index" {
				if err := os.WriteFile(filepath.Join(repo, attr), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("config", "filter."+driver+".clean", helper)
			git("config", "filter."+driver+".smudge", helper)
			if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_filter", "prj_filter", true); err == nil || !strings.Contains(err.Error(), "Git filters") {
				t.Fatal("filtered checkout was not rejected before status/checkout", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("executable filter ran during preparation", err)
			}
		})
	}
}

func TestProjectPreparationIgnoresGitEnvironmentAndUnusedFilters(t *testing.T) {
	repo, git := projectGitFixture(t)
	foreign, _ := projectGitFixture(t)
	git("config", "filter.lfs.process", "must-not-run")
	t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
	t.Setenv("GIT_WORK_TREE", foreign)
	t.Setenv("GIT_CONFIG_PARAMETERS", "invalid injected configuration")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	dir, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_environment", "prj_environment", true)
	if err != nil {
		t.Fatal("unused filters or inherited Git configuration broke an ordinary project", err)
	}
	got, err := projectGit(t.Context(), dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil || strings.TrimSpace(string(got)) != filepath.Join(repo, ".git") {
		t.Fatal("preparation was redirected to another repository", string(got), err)
	}
}

func TestProjectPreparationRejectsBranchConditionalHelpers(t *testing.T) {
	for _, event := range []string{"post-index-change", "reference-transaction"} {
		t.Run(event, func(t *testing.T) {
			repo, git := projectGitFixture(t)
			marker := filepath.Join(t.TempDir(), "executed")
			t.Setenv("BUDDY_TEST_MARKER", marker)
			helper := filepath.Join(repo, ".git", "branch-hook")
			if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf ran > \"$BUDDY_TEST_MARKER\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(repo, ".git", "branch-config")
			if err := os.WriteFile(config, []byte("[hook \"branch-only\"]\n event = "+event+"\n command = "+helper+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("config", "includeIf.onbranch:buddy/**.path", config)
			if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_branch_hook", "prj_hooks", true); err == nil || !strings.Contains(err.Error(), "configuration includes") {
				t.Fatal("branch-conditional helpers were accepted", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("branch-conditional hook executed on the host", err)
			}
		})
	}
}

func TestProjectPreparationRefusesDirtyBaseAndInitializedSubmodules(t *testing.T) {
	repo, git := projectGitFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "draft.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_dirty", "prj_dirty", true); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatal("dirty base was accepted", err)
	}
	if err := os.Remove(filepath.Join(repo, "draft.txt")); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(repo, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if data, err := exec.Command("git", "-C", child, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(string(data), err)
	}
	git("update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(git("rev-parse", "HEAD"))+",child")
	if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_submodule", "prj_submodule", true); err == nil || !strings.Contains(err.Error(), "initialized submodules") {
		t.Fatal("initialized submodule was not rejected", err)
	}
}
