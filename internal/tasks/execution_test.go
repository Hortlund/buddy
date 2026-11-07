package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureHost(caps ...string) HostProfile {
	p := HostProfile{OS: "linux", Architecture: "amd64", CPUs: 4, Slots: 1, Environment: "host"}
	for _, id := range caps {
		p.Capabilities = append(p.Capabilities, Capability{ID: id, Installed: true, Enabled: true, Ready: true})
	}
	return p
}

func TestExecutionRoutesProjectsAndCapabilitiesBeforeClaiming(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	project, err := s.SaveProject(ctx, Project{Name: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(ctx, "Build machine")
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := fixtureHost("go")
	profile.Projects = []ProjectBinding{{ProjectID: project.ID, Path: checkout}}
	if err := s.SaveHostProfile(ctx, device.ID, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveExecutionWork(ctx, device.ID, true, true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "capability-job", []Handoff{{ExecutionPlan: ExecutionPlan{ProjectID: project.ID, Role: "implementation", Requirements: []string{"go"}}, Title: "Build API", Brief: "Build the API."}})
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "capability-repeat", []Handoff{{ExecutionPlan: ExecutionPlan{ProjectID: project.ID, Role: "implementation", Requirements: []string{"go"}}, Title: "Build API", Brief: "Build the API."}})
	if err != nil || len(repeat) != 1 || !repeat[0].Reused || repeat[0].ID != jobs[0].ID {
		t.Fatalf("automatic host placement duplicated active project work: %+v %v", repeat, err)
	}
	if _, err := s.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ineligible server claimed work: %v", err)
	}
	if job, err := s.ReserveDeviceSkillWork(ctx, device.ID, true, true); err != nil || job != nil {
		t.Fatalf("old worker claimed a project it cannot interpret: %+v %v", job, err)
	}
	job, err := s.ReserveExecutionWork(ctx, device.ID, true, true)
	if err != nil || job == nil || job.ID != jobs[0].ID || job.Workspace != checkout || job.ControlToken == "" {
		t.Fatalf("incorrect execution payload: %+v %v", job, err)
	}
	if !s.AuthenticateWorkControl(ctx, job.ID, job.ControlToken) {
		t.Fatal("running assignment couldn't use its control token")
	}
	if err := s.RevokeDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if s.AuthenticateWorkControl(ctx, job.ID, job.ControlToken) {
		t.Fatal("revoked device retained work control")
	}
}

func TestExplicitHostWaitsForEnabledCapability(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	device, err := s.EnrollDevice(ctx, "Desktop")
	if err != nil {
		t.Fatal(err)
	}
	profile := fixtureHost("desktop")
	profile.Capabilities[0].Enabled = false
	if err := s.SaveHostProfile(ctx, device.ID, profile); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "desktop", []Handoff{{ExecutionPlan: ExecutionPlan{Requirements: []string{"desktop"}}, DeviceID: device.ID, Title: "Check the app", Brief: "Inspect the calculator.", ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	if job, err := s.ReserveExecutionWork(ctx, device.ID, false, false); err != nil || job != nil {
		t.Fatal("disabled capability was used", job, err)
	}
	queued, err := s.Get(ctx, jobs[0].ID)
	if err != nil || !strings.Contains(queued.Summary, "disabled") {
		t.Fatalf("missing blocker: %+v %v", queued, err)
	}
	profile.Capabilities[0].Enabled = true
	if err := s.SaveHostProfile(ctx, device.ID, profile); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ReserveExecutionWork(ctx, device.ID, false, false); err != nil || job == nil {
		t.Fatal("newly ready host did not resume its queue", err)
	}
}

func TestProjectWorkersIsolateChangesAndReturnArtifacts(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", data, err)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "initial")
	project, err := s.SaveProject(ctx, Project{Name: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	profile := fixtureHost("go")
	profile.Projects = []ProjectBinding{{ProjectID: project.ID, Path: repo}}
	if err := s.SaveHostProfile(ctx, "server", profile); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "project", []Handoff{{ExecutionPlan: ExecutionPlan{ProjectID: project.ID, Role: "implementation", Requirements: []string{"go"}}, Title: "Implement health", Brief: "Implement a health check."}})
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(s, nil, func(ctx context.Context, turn Turn, emit Emit) (string, error) {
		if turn.Agent.Workspace == repo || turn.OutputDirectory == "" {
			t.Error("worker did not receive an isolated checkout and outbox")
		}
		return "Created `main.go` and a result.", os.WriteFile(filepath.Join(turn.OutputDirectory, "result.md"), []byte("# Done\n"), 0600)
	})
	lifetime, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(lifetime) }()
	defer func() { stop(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := s.Get(ctx, jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == Succeeded {
			break
		}
		if task.Status == Failed {
			t.Fatalf("worker failed: %+v", task)
		}
		time.Sleep(10 * time.Millisecond)
	}
	detail, err := s.WorkDetails(ctx, DefaultConversationID, jobs[0].ID)
	if err != nil || detail.Status != Succeeded || len(detail.Sessions) != 1 || len(detail.Artifacts) != 1 || detail.Sessions[0].Branch == "" {
		t.Fatalf("missing execution history: %+v %v", detail, err)
	}
	artifact, err := s.WorkArtifact(ctx, detail.ID, detail.Artifacts[0].ID)
	if err != nil || string(artifact.Data) != "# Done\n" {
		t.Fatal("deliverable was not returned", err)
	}
	if !strings.Contains(detail.Result, "buddy://artifacts/") {
		t.Fatal("the chat result lost its deliverable link")
	}
	if _, err := os.Stat(filepath.Join(repo, "result.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worker wrote into the original checkout")
	}
	runtime := &CodexRuntime{}
	preview, err := runtime.WorkspaceItem(ctx, s, DefaultAgentID, "", "main.go", jobs[0].ID)
	if err != nil || !preview.ReadOnly || preview.Text != "package main\n" {
		t.Fatalf("project reference opened the wrong checkout: %+v %v", preview, err)
	}
	if err := s.ArchiveProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactOutboxRejectsLinksAndNeverScansTheCheckout(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := ArtifactOutbox(root, "tsk_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if files, err := CollectWorkArtifacts(outbox); err != nil || len(files) != 0 {
		t.Fatal("exported an unrelated checkout file", err)
	}
	if err := os.Symlink(filepath.Join(root, "private.txt"), filepath.Join(outbox, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectWorkArtifacts(outbox); err == nil {
		t.Fatal("exported a symlink")
	}
}

func TestWorkerControlIsScopedIdempotentAndRefreshesTeam(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	peer, err := s.CreateAgent(ctx, Agent{Name: "Reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateGroup(ctx, GroupRequest{Name: "Team", Members: []string{DefaultAgentID, peer.ID}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.QueueVoiceAssignments(ctx, group.ID, DefaultAgentID, "call", "control", []Handoff{{Title: "Investigate", Brief: "Investigate a problem."}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimLane(ctx, true); err != nil {
		t.Fatal(err)
	}
	job := jobs[0].ID
	call := func(name, input string) (any, error) { return s.controlWork(ctx, job, name, json.RawMessage(input)) }
	if _, err := call("post_update", `{"text":"Investigating the failure","request_id":"update-1"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call("post_update", `{"text":"Investigating the failure","request_id":"update-1"}`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE task_id=? AND stream='work_update'", job).Scan(&count); err != nil || count != 1 {
		t.Fatal("update retry duplicated an event", count, err)
	}
	if _, err := call("delegate", `{"agent_id":"`+peer.ID+`","title":"Review findings","brief":"Review this assignment's findings.","read_only":true,"request_id":"delegate-1"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call("inspect_work", `{"job_id":"tsk_unknown"}`); !errors.Is(err, ErrNotFound) {
		t.Fatal("control inspected unknown work", err)
	}
	if err := s.SetGroupLead(ctx, group.ID, peer.ID); err != nil {
		t.Fatal(err)
	}
	value, err := call("team_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	team := value.(map[string]any)["team"].(TeamContext)
	if team.LeadID != peer.ID {
		t.Fatal("worker retained stale leadership")
	}
	if _, err := s.cancel(ctx, job, false); err != nil {
		t.Fatal(err)
	}
	work, err := s.WorkOverview(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range work {
		if child.ID != job && child.Status != Cancelled {
			t.Fatalf("stopped assignment left delegated work active: %+v", child)
		}
	}
	if _, err := call("post_update", `{"text":"Too late","request_id":"update-2"}`); err == nil {
		t.Fatal("stopped worker retained its mutation tools")
	}
}

func TestProjectWorkspaceRejectsForeignResumeAndPathTraversal(t *testing.T) {
	ctx := t.Context()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if data, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", data, err)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	if _, _, err := PrepareProjectWorkspace(ctx, repo, "../escape", "prj_test", true); err == nil {
		t.Fatal("accepted an invalid assignment path")
	}
	dir, branch, err := PrepareProjectWorkspace(ctx, repo, "tsk_resume", "prj_test", true)
	if err != nil {
		t.Fatal(err)
	}
	if resumed, resumedBranch, err := PrepareProjectWorkspace(ctx, repo, "tsk_resume", "prj_test", true); err != nil || resumed != dir || resumedBranch != branch {
		t.Fatal("valid worktree could not resume", err)
	}
	if data, err := exec.CommandContext(ctx, "git", "-C", dir, "switch", "-c", "foreign").CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", data, err)
	}
	if _, _, err := PrepareProjectWorkspace(ctx, repo, "tsk_resume", "prj_test", true); err == nil {
		t.Fatal("resumed a different branch under the worker's identity")
	}
}

func TestAutomationApprovalDoesNotSurviveChangedProvider(t *testing.T) {
	ctx := t.Context()
	command := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launch, err := inspectMCPLaunch(ctx, command, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &CodexRuntime{}
	setup := AutomationSetup{Provider: "cua", Command: command, AllowInteractions: true, Tools: []string{"list_apps"}, Fingerprint: launch.fingerprint}
	if err := runner.ConfigureExecution(ctx, ExecutionConfig{Automation: []AutomationSetup{setup}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\necho changed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.executionMCP(ctx, Turn{ExecutionPlan: ExecutionPlan{Requirements: []string{"desktop"}}}); err == nil {
		t.Fatal("changed executable inherited an old tool approval")
	}
	if err := runner.ConfigureExecution(ctx, ExecutionConfig{Automation: []AutomationSetup{setup}}); err == nil {
		t.Fatal("reloading stale host configuration approved a changed executable")
	}
	if err := runner.LoadHostExecution(ctx, ExecutionConfig{Automation: []AutomationSetup{setup}}); err != nil || len(runner.ExecutionConfig().Automation) != 0 {
		t.Fatal("a stale optional provider prevented the host from serving other work", err)
	}
}
