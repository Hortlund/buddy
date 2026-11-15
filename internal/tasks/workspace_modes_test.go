package tasks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectWorkspaceResumeKeepsExistingWorktreeWithDirtyBase(t *testing.T) {
	for _, mode := range []string{"auto", "worktree"} {
		t.Run(mode, func(t *testing.T) {
			repo, _ := projectGitFixture(t)
			ctx := t.Context()
			dir, branch, err := PrepareProjectWorkspace(ctx, repo, "tsk_resume", "prj_resume", true, mode)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "worker-progress.txt"), []byte("unfinished worker change"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "user-draft.txt"), []byte("user change"), 0600); err != nil {
				t.Fatal(err)
			}
			resumed, resumedBranch, err := PrepareProjectWorkspace(ctx, repo, "tsk_resume", "prj_resume", true, mode)
			if err != nil || resumed != dir || resumedBranch != branch {
				t.Fatalf("resume switched workspace: %q %q, %v", resumed, resumedBranch, err)
			}
			if data, err := os.ReadFile(filepath.Join(resumed, "worker-progress.txt")); err != nil || string(data) != "unfinished worker change" {
				t.Fatal("worker progress was lost", err)
			}
			if data, err := os.ReadFile(filepath.Join(repo, "user-draft.txt")); err != nil || string(data) != "user change" {
				t.Fatal("user changes were altered", err)
			}
			if readOnly, readOnlyBranch, err := PrepareProjectWorkspace(ctx, repo, "tsk_resume", "prj_resume", false, mode); err != nil || readOnly != dir || readOnlyBranch != branch {
				t.Fatal("disabling writes changed the resumed workspace", err)
			}
			if output, err := exec.CommandContext(ctx, "git", "-C", dir, "switch", "-c", "foreign").CombinedOutput(); err != nil {
				t.Fatalf("switch branch: %s, %v", output, err)
			}
			if _, _, err := PrepareProjectWorkspace(ctx, repo, "tsk_resume", "prj_resume", true, mode); err == nil {
				t.Fatal("dirty base bypassed validation of an existing foreign worktree")
			}
		})
	}
}

func TestProjectWorkspaceResumeRejectsLostSourceGitMetadata(t *testing.T) {
	repo, _ := projectGitFixture(t)
	if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_resume", "prj_resume", true); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(repo, ".git-moved")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_resume", "prj_resume", true); err == nil {
		t.Fatal("missing Git metadata silently changed the resumed workspace")
	}
}

func TestResumedProjectKeepsResolvedWorkspaceChoice(t *testing.T) {
	for _, hostKind := range []string{"server", "remote"} {
		for _, initialMode := range []string{"checkout", "worktree"} {
			t.Run(hostKind+"/"+initialMode, func(t *testing.T) {
				ctx, store := t.Context(), teamTestStore(t)
				repo, git := projectGitFixture(t)
				if initialMode == "checkout" {
					if err := os.WriteFile(filepath.Join(repo, "draft.txt"), []byte("draft"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				project, err := store.SaveProject(ctx, Project{Name: "Resume fixture"})
				if err != nil {
					t.Fatal(err)
				}
				host := "server"
				if hostKind == "remote" {
					device, err := store.EnrollDevice(ctx, "Worker")
					if err != nil {
						t.Fatal(err)
					}
					host = device.ID
				}
				if err := store.SaveHostProfile(ctx, host, HostProfile{OS: "linux", Architecture: "amd64", CPUs: 2, Environment: "host", Projects: []ProjectBinding{{ProjectID: project.ID, Path: repo}}}); err != nil {
					t.Fatal(err)
				}
				agent, err := store.Agent(ctx, DefaultAgentID)
				if err != nil {
					t.Fatal(err)
				}
				agent.AllowEdits = true
				if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
					t.Fatal(err)
				}
				jobs, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "resume-choice", []Handoff{{DeviceID: host, Title: "Implement", Brief: "Implement and verify the change.", ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}})
				if err != nil {
					t.Fatal(err)
				}
				claim := func() ExecutionPlan {
					t.Helper()
					if host == "server" {
						task, err := store.claimLane(ctx, true)
						if err != nil || task.ID != jobs[0].ID {
							t.Fatalf("claim local job: %+v, %v", task, err)
						}
						turn, err := store.turn(ctx, task)
						if err != nil {
							t.Fatal(err)
						}
						return turn.ExecutionPlan
					}
					job, err := store.ReserveWorkerPlanningWork(ctx, host, false, true)
					if err != nil || job == nil || job.ID != jobs[0].ID {
						t.Fatalf("claim remote job: %+v, %v", job, err)
					}
					return job.ExecutionPlan
				}
				plan := claim()
				dir, branch, err := PrepareProjectWorkspace(ctx, repo, jobs[0].ID, project.ID, true, plan.WorkspaceMode)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.StartWorkerSession(ctx, jobs[0].ID, host, dir, branch); err != nil {
					t.Fatal(err)
				}
				if err := store.finishTurn(ctx, jobs[0].ID, NeedsInput, "Waiting.", "Which target?", nil); err != nil {
					t.Fatal(err)
				}
				if initialMode == "checkout" {
					git("add", "draft.txt")
					git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "save user work")
				} else if err := os.WriteFile(filepath.Join(repo, "draft.txt"), []byte("user work"), 0600); err != nil {
					t.Fatal(err)
				}
				detail, err := store.WorkDetails(ctx, DefaultConversationID, jobs[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.AnswerWorkInput(ctx, DefaultConversationID, detail.InputID, "The API", "target", nil); err != nil {
					t.Fatal(err)
				}
				plan = claim()
				if plan.WorkspaceMode != initialMode {
					t.Fatalf("resolved mode changed to %q, want %q", plan.WorkspaceMode, initialMode)
				}
				resumed, resumedBranch, err := PrepareProjectWorkspace(ctx, repo, jobs[0].ID, project.ID, true, plan.WorkspaceMode)
				if err != nil || resumed != dir || resumedBranch != branch {
					t.Fatalf("resume changed workspace: %q %q, %v", resumed, resumedBranch, err)
				}
				requested, err := readExecutionPlan(ctx, store.db, jobs[0].ID)
				if err != nil || requested.WorkspaceMode != "" {
					t.Fatal("resolved placement overwrote the requested automatic plan", err)
				}
			})
		}
	}
}

func TestCheckoutSessionsRejectOverlappingRoots(t *testing.T) {
	for _, host := range []string{"server", "remote-worker"} {
		for _, childFirst := range []bool{false, true} {
			t.Run(host+"/child-first="+fmt.Sprint(childFirst), func(t *testing.T) {
				ctx, store := t.Context(), teamTestStore(t)
				agent, err := store.Agent(ctx, DefaultAgentID)
				if err != nil {
					t.Fatal(err)
				}
				agent.AllowEdits = true
				if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
					t.Fatal(err)
				}
				jobs, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "overlap", []Handoff{
					{Title: "First", Brief: "First task.", ExecutionPlan: ExecutionPlan{WorkspaceMode: "checkout"}},
					{Title: "Second", Brief: "Second task.", ExecutionPlan: ExecutionPlan{WorkspaceMode: "checkout"}},
					{Title: "Separate", Brief: "Separate task.", ExecutionPlan: ExecutionPlan{WorkspaceMode: "checkout"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				for _, job := range jobs {
					if _, err := store.db.ExecContext(ctx, "UPDATE tasks SET status='running' WHERE id=?", job.ID); err != nil {
						t.Fatal(err)
					}
				}
				root := filepath.Join(t.TempDir(), "repo")
				child, sibling := filepath.Join(root, "service"), root+"-other"
				if host == "server" {
					for _, dir := range []string{child, sibling} {
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					// These paths belong to another machine and need not exist here.
					root, child, sibling = "/worker/projects/repo", "/worker/projects/repo/service", "/worker/projects/repo-other"
				}
				first, second := root, child
				if childFirst {
					first, second = second, first
				}
				if err := store.StartWorkerSession(ctx, jobs[0].ID, host, first, ""); err != nil {
					t.Fatal(err)
				}
				if err := store.StartWorkerSession(ctx, jobs[1].ID, host, second, ""); err == nil || !strings.Contains(err.Error(), "Another worker") {
					t.Fatal("overlapping checkout writer started", err)
				}
				if err := store.StartWorkerSession(ctx, jobs[2].ID, host, sibling, ""); err != nil {
					t.Fatal("a similar directory name was treated as overlap", err)
				}
				if err := store.finishTurn(ctx, jobs[0].ID, Succeeded, "Finished.", "Done.", nil); err != nil {
					t.Fatal(err)
				}
				if err := store.StartWorkerSession(ctx, jobs[1].ID, host, second, ""); err != nil {
					t.Fatal("finished session still blocked the checkout", err)
				}
			})
		}
	}
}

func TestProjectWorkspaceModePreservesPendingChanges(t *testing.T) {
	repo, git := projectGitFixture(t)
	draft := filepath.Join(repo, "draft.txt")
	if err := os.WriteFile(draft, []byte("user work"), 0600); err != nil {
		t.Fatal(err)
	}
	if dir, branch, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_auto", "prj_test", true); err != nil || dir != repo || branch != "" {
		t.Fatal("automatic planning hid pending changes", err)
	}
	if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_isolated", "prj_test", true, "worktree"); err == nil {
		t.Fatal("explicit isolation ignored pending changes")
	}
	dir, branch, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_checkout", "prj_test", true, "checkout")
	if err != nil || dir != repo || branch != "" {
		t.Fatalf("checkout selection: %q %q, %v", dir, branch, err)
	}
	if data, err := os.ReadFile(draft); err != nil || string(data) != "user work" {
		t.Fatal("pending work changed", err)
	}
	if !strings.Contains(git("status", "--porcelain"), "draft.txt") {
		t.Fatal("pending work was hidden from Git")
	}
	if _, _, err := PrepareProjectWorkspace(t.Context(), repo, "tsk_bad", "prj_test", true, "invalid"); err == nil {
		t.Fatal("unknown workspace mode accepted")
	}
	if _, _, err := PrepareProjectWorkspace(t.Context(), t.TempDir(), "tsk_new", "prj_test", true, "worktree"); err == nil {
		t.Fatal("promised a worktree for a new non-Git project")
	}
}

func TestWorkspaceModeSurvivesRoutingAndDoesNotReuseDifferentPlan(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	assign := func(key, mode string) AssignedWork {
		t.Helper()
		jobs, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", key, []Handoff{{
			Title: "Build", Brief: "Build the code.", ExecutionPlan: ExecutionPlan{WorkspaceMode: mode},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return jobs[0]
	}
	checkout := assign("checkout", "checkout")
	automatic := assign("automatic", "auto")
	if checkout.ID == automatic.ID {
		t.Fatal("different workspace plans reused one assignment")
	}
	turn, err := store.turn(ctx, checkout.Task)
	if err != nil || turn.WorkspaceMode != "checkout" {
		t.Fatalf("lost checkout mode: %+v, %v", turn.ExecutionPlan, err)
	}
	if repeated := assign("repeat", ""); repeated.ID != automatic.ID {
		t.Fatal("equivalent automatic plans did not reuse work")
	}
}

func TestCheckoutSessionsExcludeConflictingWriters(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent.AllowEdits, agent.Workspace = true, t.TempDir()
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "checkout-writers", []Handoff{
		{Title: "First", Brief: "Implement the first part.", ExecutionPlan: ExecutionPlan{WorkspaceMode: "checkout"}},
		{Title: "Second", Brief: "Implement the second part.", ExecutionPlan: ExecutionPlan{WorkspaceMode: "checkout"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if _, err := store.db.ExecContext(ctx, "UPDATE tasks SET status='running' WHERE id=?", job.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.StartWorkerSession(ctx, jobs[0].ID, "server", agent.Workspace, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.StartWorkerSession(ctx, jobs[1].ID, "server", agent.Workspace, ""); err == nil || !strings.Contains(err.Error(), "Another worker") {
		t.Fatal("conflicting writer started", err)
	}
	if err := store.finishTurn(ctx, jobs[0].ID, Succeeded, "Finished.", "Done.", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.StartWorkerSession(ctx, jobs[1].ID, "server", agent.Workspace, ""); err != nil {
		t.Fatal("completed session blocked the checkout", err)
	}
}

func TestProjectCheckoutAllowsMissingOptionalProtectedDirectories(t *testing.T) {
	root := t.TempDir()
	reserved := filepath.Join(root, "future", "migration")
	project := t.TempDir()
	runtime := &CodexRuntime{migrationDir: reserved}
	if err := runtime.ConfigureExecution(t.Context(), ExecutionConfig{Projects: []ProjectBinding{{ProjectID: "prj_test", Path: project}}}); err != nil {
		t.Fatal("valid checkout rejected before first migration", err)
	}
	if err := runtime.ConfigureExecution(t.Context(), ExecutionConfig{Projects: []ProjectBinding{{ProjectID: "prj_test", Path: root}}}); err == nil {
		t.Fatal("project was allowed to contain the future migration directory")
	}
	runtime = &CodexRuntime{protected: []string{filepath.Join(root, "missing-runtime")}}
	if err := runtime.ConfigureExecution(t.Context(), ExecutionConfig{Projects: []ProjectBinding{{ProjectID: "prj_test", Path: project}}}); err == nil {
		t.Fatal("missing mandatory runtime files were ignored")
	}
}
