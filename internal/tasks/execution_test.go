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

	"buddy/internal/localconfig"
)

func fixtureHost(caps ...string) HostProfile {
	p := HostProfile{OS: "linux", Architecture: "amd64", CPUs: 4, Slots: 1, Environment: "host"}
	for _, id := range caps {
		p.Capabilities = append(p.Capabilities, Capability{ID: id, Installed: true, Enabled: true, Ready: true})
	}
	return p
}

func TestBudApprovalKeepsProjectCheckout(t *testing.T) {
	for _, route := range []string{"server", "chosen during clarification", "worker", "automatic", "pool"} {
		t.Run(route, func(t *testing.T) {
			ctx, s := t.Context(), teamTestStore(t)
			project, err := s.SaveProject(ctx, Project{Name: "Service"})
			if err != nil {
				t.Fatal(err)
			}
			host, target := "server", "server"
			if route == "worker" || route == "automatic" || route == "pool" {
				device, err := s.EnrollDevice(ctx, "Build Mac")
				if err != nil {
					t.Fatal(err)
				}
				host, target = device.ID, device.ID
				if route == "automatic" {
					target = ""
				} else if route == "pool" {
					target = DevicePool
				}
				if _, err := s.ReserveExecutionWork(ctx, host, true, true); err != nil {
					t.Fatal(err)
				}
			}
			setCheckout := func(path string) {
				t.Helper()
				profile := fixtureHost()
				profile.Projects = []ProjectBinding{{ProjectID: project.ID, Path: path}}
				if err := s.SaveHostProfile(ctx, host, profile); err != nil {
					t.Fatal(err)
				}
			}
			original, replacement := t.TempDir(), t.TempDir()
			if route != "chosen during clarification" {
				setCheckout(original)
			}
			draft := BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review release notes."}
			handoff := Handoff{AgentID: newBudAssignee, DeviceID: target, Title: "Edit release notes", Brief: "Update the release notes.",
				ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}
			const chat = DefaultConversationID
			var task string
			if host == "server" {
				request, _, err := s.Send(ctx, chat, "Draft Finch and assign the release notes.", "draft-project-work")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.claimLane(ctx, false); err != nil {
					t.Fatal(err)
				}
				action := &TurnAction{
					Card:             &ChoiceCard{Question: "How should Finch report back?", Options: []string{"Summary", "Detailed"}},
					BudClarification: true, Handoffs: []Handoff{handoff},
				}
				if route == "chosen during clarification" {
					action.Card = &ChoiceCard{Question: "Which project should Finch work on?", Options: []string{"Service", "Other"}}
					action.Handoffs = nil
				}
				if err := s.finishTurn(ctx, request.ID, Succeeded, "Replied.", "", action); err != nil {
					t.Fatal(err)
				}
				if route == "chosen during clarification" {
					setCheckout(original)
				} else {
					setCheckout(replacement)
				}
				answer, _, err := s.AnswerCard(ctx, chat, request.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.claimLane(ctx, false); err != nil {
					t.Fatal(err)
				}
				if err := s.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", &TurnAction{
					BudProposal: &draft, Handoffs: []Handoff{handoff},
				}); err != nil {
					t.Fatal(err)
				}
				task = answer.ID
				setCheckout(replacement)
			} else {
				if _, _, _, err := s.SaveVoiceBud(ctx, chat, "call", DefaultAgentID, "Draft a Bud named Finch.", draft, handoff); err != nil {
					t.Fatal(err)
				}
				current, err := s.Conversation(ctx, chat, 0)
				if err != nil || len(current.Messages) != 1 || current.Messages[0].BudProposal == nil {
					t.Fatalf("missing draft: %+v, %v", current.Messages, err)
				}
				task = current.Messages[0].TaskID
				setCheckout(replacement)
			}
			if _, err := s.CreateProposedBud(ctx, chat, task, CreateBudWithWork); err == nil || !strings.Contains(err.Error(), "checkout") {
				t.Fatalf("approval accepted the replacement checkout: %v", err)
			}
			if jobs, err := s.WorkOverview(ctx, chat); err != nil || len(jobs) != 0 {
				t.Fatalf("failed approval left jobs: %+v, %v", jobs, err)
			}
			setCheckout(original)
			bud, err := s.CreateProposedBud(ctx, chat, task, CreateBudWithWork)
			if err != nil || bud.ID == "" {
				t.Fatalf("could not approve the original checkout: %+v, %v", bud, err)
			}
			setCheckout(replacement)
			if host != "server" {
				if job, err := s.ReserveExecutionWork(ctx, host, true, true); err != nil || job != nil {
					t.Fatalf("worker claimed work after its checkout changed: %+v, %v", job, err)
				}
				setCheckout(original)
				job, err := s.ReserveExecutionWork(ctx, host, true, true)
				if err != nil || job == nil || job.Workspace != original {
					t.Fatalf("worker did not claim the original checkout: %+v, %v", job, err)
				}
				handoff.AgentID = bud.ID
				repeated, err := s.QueueVoiceAssignments(ctx, chat, DefaultAgentID, "call", "repeat", []Handoff{handoff})
				if err != nil || len(repeated) != 1 || !repeated[0].Reused || repeated[0].ID != job.ID {
					t.Fatalf("repeat did not reuse the active job: %+v, %v", repeated, err)
				}
				setCheckout(replacement)
				changed, err := s.QueueVoiceAssignments(ctx, chat, DefaultAgentID, "call", "changed", []Handoff{handoff})
				if err != nil || len(changed) != 1 || changed[0].Reused || changed[0].ID == job.ID {
					t.Fatalf("changed checkout reused the existing job: %+v, %v", changed, err)
				}
				if err := s.FinishDeviceJob(ctx, host, job.ID, DeviceResult{Lease: job.Lease, Status: Succeeded}); err != nil {
					t.Fatal(err)
				}
				job, err = s.ReserveExecutionWork(ctx, host, true, true)
				if err != nil || job == nil || job.ID != changed[0].ID || job.Workspace != replacement {
					t.Fatalf("worker did not claim the standalone job at the replacement checkout: %+v, %v", job, err)
				}
				setCheckout(t.TempDir())
				next, err := s.QueueVoiceAssignments(ctx, chat, DefaultAgentID, "call", "changed-again", []Handoff{handoff})
				if err != nil || len(next) != 1 || next[0].Reused || next[0].ID == job.ID {
					t.Fatalf("new checkout reused the active standalone job: %+v, %v", next, err)
				}
			} else {
				if _, err := s.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
					t.Fatalf("server claimed work after its checkout changed: %v", err)
				}
				setCheckout(original)
				if job, err := s.claimLane(ctx, true); err != nil || job.Repository != original {
					t.Fatalf("server did not claim the original checkout: %+v, %v", job, err)
				}
			}
		})
	}
}

func TestBudClarificationCanSelectNewWorkerCheckout(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	project, err := s.SaveProject(ctx, Project{Name: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	original := addCheckoutWorker(t, s, "Original worker")
	selected := addCheckoutWorker(t, s, "Selected worker")
	setWorkerCheckout(t, s, original, project.ID, t.TempDir())
	setWorkerCheckout(t, s, selected, project.ID, t.TempDir())
	const chat = DefaultConversationID
	handoff := Handoff{AgentID: newBudAssignee, DeviceID: original, Title: "Edit release notes", Brief: "Update the release notes.",
		ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}
	request, _, err := s.Send(ctx, chat, "Draft Finch and assign the release notes.", "draft-worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s.finishTurn(ctx, request.ID, Succeeded, "Which worker?", "", &TurnAction{
		Card:             &ChoiceCard{Question: "Which worker?", Options: []string{"Selected worker", "Original worker"}},
		BudClarification: true, Handoffs: []Handoff{handoff},
	}); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	setWorkerCheckout(t, s, selected, project.ID, checkout)
	answer, _, err := s.AnswerCard(ctx, chat, request.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	handoff.DeviceID = selected
	if err := s.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", &TurnAction{
		BudProposal: &BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review release notes."},
		Handoffs:    []Handoff{handoff},
	}); err != nil {
		t.Fatal(err)
	}
	setWorkerCheckout(t, s, selected, project.ID, t.TempDir())
	if _, err := s.CreateProposedBud(ctx, chat, answer.ID, CreateBudWithWork); err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("revised draft accepted a replacement checkout: %v", err)
	}
	setWorkerCheckout(t, s, selected, project.ID, checkout)
	bud, err := s.CreateProposedBud(ctx, chat, answer.ID, CreateBudWithWork)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.ReserveExecutionWork(ctx, selected, false, false)
	if err != nil || job == nil || job.Agent.ID != bud.ID || job.Workspace != checkout {
		t.Fatalf("newly selected worker could not claim the approved work: %+v, %v", job, err)
	}
}

func TestAutomaticDraftWorkerClaimsApprovedCheckout(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	project, err := s.SaveProject(ctx, Project{Name: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	worker := addCheckoutWorker(t, s, "Build worker")
	if _, err := s.ReserveExecutionWork(ctx, worker, true, true); err != nil {
		t.Fatal(err)
	}
	approved, alternate := t.TempDir(), t.TempDir()
	const chat = DefaultConversationID
	turn, _, err := s.Send(ctx, chat, "Draft Finch and review the release notes.", "draft-route")
	if err != nil {
		t.Fatal(err)
	}
	for i, route := range []string{"", worker, ""} {
		checkout := approved
		if i == 1 {
			checkout = alternate
		}
		setWorkerCheckout(t, s, worker, project.ID, checkout)
		if _, err := s.claimLane(ctx, false); err != nil {
			t.Fatal(err)
		}
		action := &TurnAction{Handoffs: []Handoff{{AgentID: newBudAssignee, DeviceID: route, Title: "Review release notes",
			Brief: "Review the release notes.", ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}}}
		if i == 2 {
			action.BudProposal = &BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review release notes."}
		} else {
			action.Card = &ChoiceCard{Question: "Where should Finch run?", Options: []string{"Change placement", "Keep placement"}}
			action.BudClarification = true
		}
		if err := s.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "", action); err != nil {
			t.Fatal(err)
		}
		if action.Card != nil {
			turn, _, err = s.AnswerCard(ctx, chat, turn.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	bud, err := s.CreateProposedBud(ctx, chat, turn.ID, CreateBudWithWork)
	if err != nil {
		t.Fatal(err)
	}
	repeat := Handoff{AgentID: bud.ID, Title: "Review release notes", Brief: "Review the release notes.",
		ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}
	jobs, err := s.QueueVoiceAssignments(ctx, chat, DefaultAgentID, "call", "repeat-automatic", []Handoff{repeat})
	if err != nil || len(jobs) != 1 || !jobs[0].Reused {
		t.Fatalf("repeating the automatic request did not reuse its job: %+v, %v", jobs, err)
	}
	setWorkerCheckout(t, s, worker, project.ID, alternate)
	if job, err := s.ReserveExecutionWork(ctx, worker, true, true); err != nil || job != nil {
		t.Fatalf("worker claimed the intermediate checkout: %+v, %v", job, err)
	}
	setWorkerCheckout(t, s, worker, project.ID, approved)
	job, err := s.ReserveExecutionWork(ctx, worker, true, true)
	if err != nil || job == nil || job.ID != jobs[0].ID || job.Workspace != approved {
		t.Fatalf("worker could not claim the approved checkout: %+v, %v", job, err)
	}
	if err := s.FinishDeviceJob(ctx, worker, job.ID, DeviceResult{Lease: job.Lease, Status: NeedsInput, Reply: "Which release?"}); err != nil {
		t.Fatal(err)
	}
	// Before checkout selection was fixed, this paused job could have used the
	// alternate checkout. Reproduce the dispatch record that an upgrade keeps.
	if _, err := s.db.ExecContext(ctx, "UPDATE device_project_checkouts SET path=? WHERE task_id=?", alternate, job.ID); err != nil {
		t.Fatal(err)
	}
	answerCheckoutQuestion(t, s, job.ID)
	if resumed, err := s.ReserveExecutionWork(ctx, worker, true, true); err != nil || resumed != nil {
		t.Fatalf("paused job resumed on a different checkout: %+v, %v", resumed, err)
	}
	var originalPath string
	if err := s.db.QueryRowContext(ctx, "SELECT path FROM device_project_checkouts WHERE task_id=?", job.ID).Scan(&originalPath); err != nil || originalPath != alternate {
		t.Fatalf("resume rewrote the dispatch record: %q, %v", originalPath, err)
	}
	queued, err := s.Get(ctx, job.ID)
	if err != nil || queued.Status != Queued || !strings.Contains(queued.Summary, "start") || !strings.Contains(queued.Summary, "checkout") {
		t.Fatalf("paused job did not explain the checkout blocker: %+v, %v", queued, err)
	}
	restarted, err := s.QueueVoiceAssignments(ctx, chat, DefaultAgentID, "call", "restart-automatic", []Handoff{repeat})
	if err != nil || len(restarted) != 1 || restarted[0].ID == job.ID {
		t.Fatalf("could not start new work on the approved checkout: %+v, %v", restarted, err)
	}
	if next, err := s.ReserveExecutionWork(ctx, worker, true, true); err != nil || next == nil || next.ID != restarted[0].ID || next.Workspace != approved {
		t.Fatalf("worker could not claim fresh work after a blocked resume: %+v, %v", next, err)
	}
}

func TestDeviceProjectDeliveryRetriesOnCurrentCheckout(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	project, err := s.SaveProject(ctx, Project{Name: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	worker := addCheckoutWorker(t, s, "Build worker")
	initial, current := t.TempDir(), t.TempDir()
	setWorkerCheckout(t, s, worker, project.ID, initial)
	const chat = DefaultConversationID
	turn, _, err := s.SendWithAttachments(ctx, chat, "", "Review the release notes.", "delivery-retry", []Attachment{{Name: "notes.txt", Data: []byte("Notes")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	handoff := Handoff{DeviceID: worker, Title: "Review release notes", Brief: "Review the attached notes.",
		ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}
	if err := s.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "", &TurnAction{Handoff: &handoff}); err != nil {
		t.Fatal(err)
	}
	first, err := s.ReserveExecutionWork(ctx, worker, false, false)
	if err != nil || first == nil || !first.NeedsStart || first.Workspace != initial {
		t.Fatalf("expected a delivery reservation: %+v, %v", first, err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE device_runs SET expires=? WHERE task_id=?", time.Now().Add(-time.Second).UnixMicro(), first.ID); err != nil {
		t.Fatal(err)
	}
	setWorkerCheckout(t, s, worker, project.ID, current)
	retry, err := s.ReserveExecutionWork(ctx, worker, false, false)
	if err != nil || retry == nil || retry.ID != first.ID || !retry.NeedsStart || retry.Lease == first.Lease || retry.Workspace != current || len(retry.Attachments) != 1 {
		t.Fatalf("expired delivery did not retry on the current checkout: %+v, %v", retry, err)
	}
	if err := s.StartDeviceJob(ctx, worker, first.ID, first.Lease); !errors.Is(err, ErrInactive) {
		t.Fatalf("expired reservation could still start: %v", err)
	}
	if err := s.StartDeviceJob(ctx, worker, retry.ID, retry.Lease); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDeviceJob(ctx, worker, retry.ID, DeviceResult{Lease: retry.Lease, Status: NeedsInput, Reply: "Which release?"}); err != nil {
		t.Fatal(err)
	}
	answerCheckoutQuestion(t, s, retry.ID)
	setWorkerCheckout(t, s, worker, project.ID, initial)
	if job, err := s.ReserveExecutionWork(ctx, worker, false, false); err != nil || job != nil {
		t.Fatalf("resumed work returned to the checkout from the expired reservation: %+v, %v", job, err)
	}
	setWorkerCheckout(t, s, worker, project.ID, current)
	if job, err := s.ReserveExecutionWork(ctx, worker, false, false); err != nil || job == nil || job.ID != retry.ID || job.Workspace != current {
		t.Fatalf("work could not resume on the checkout that started: %+v, %v", job, err)
	}
}

func TestDeviceProjectWorkResumesOnItsOriginalCheckout(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	project, err := s.SaveProject(ctx, Project{Name: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	worker := addCheckoutWorker(t, s, "Build worker")
	original := t.TempDir()
	setWorkerCheckout(t, s, worker, project.ID, original)
	job, err := s.QueueVoiceHandoff(ctx, DefaultConversationID, "call", "review", Handoff{
		AgentID: DefaultAgentID, DeviceID: worker, Title: "Review release notes", Brief: "Review the release notes.",
		ExecutionPlan: ExecutionPlan{ProjectID: project.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ReserveExecutionWork(ctx, worker, false, false)
	if err != nil || first == nil || first.ID != job.ID || first.Workspace != original {
		t.Fatalf("worker did not claim the original checkout: %+v, %v", first, err)
	}
	if err := s.FinishDeviceJob(ctx, worker, first.ID, DeviceResult{Lease: first.Lease, Status: NeedsInput, Reply: "Which release?"}); err != nil {
		t.Fatal(err)
	}
	setWorkerCheckout(t, s, worker, project.ID, t.TempDir())
	answerCheckoutQuestion(t, s, first.ID)
	if next, err := s.ReserveExecutionWork(ctx, worker, false, false); err != nil || next != nil {
		t.Fatalf("worker resumed in a different checkout: %+v, %v", next, err)
	}
	setWorkerCheckout(t, s, worker, project.ID, original)
	if next, err := s.ReserveExecutionWork(ctx, worker, false, false); err != nil || next == nil || next.ID != first.ID ||
		next.Workspace != original || next.Lease == first.Lease || !strings.Contains(next.Brief, "v1.2") {
		t.Fatalf("worker lost the original checkout or latest answer: %+v, %v", next, err)
	}
}

func answerCheckoutQuestion(t *testing.T, s *Store, job string) {
	t.Helper()
	ctx := t.Context()
	detail, err := s.WorkDetails(ctx, DefaultConversationID, job)
	if err != nil || detail.InputID == "" {
		t.Fatalf("expected a question from work: %+v, %v", detail, err)
	}
	if resumed, err := s.AnswerWorkInput(ctx, DefaultConversationID, detail.InputID, "Use v1.2.", "checkout-answer", nil); err != nil || !resumed {
		t.Fatalf("could not answer the worker: %v, %v", resumed, err)
	}
}

func TestDraftPoolPrerequisiteIgnoresRevokedWorkers(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	project, err := s.SaveProject(ctx, Project{Name: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	worker := addCheckoutWorker(t, s, "Build worker")
	setWorkerCheckout(t, s, worker, project.ID, t.TempDir())
	if _, err := s.ReserveExecutionWork(ctx, worker, true, true); err != nil {
		t.Fatal(err)
	}
	const chat = DefaultConversationID
	prerequisite := Handoff{AgentID: DefaultAgentID, DeviceID: DevicePool, Title: "Collect release notes", Brief: "Collect the draft notes.",
		ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}
	followup := Handoff{AgentID: newBudAssignee, Title: "Review release notes", Brief: "Review the collected notes.", After: []int{1}}
	approve := func(name string) {
		t.Helper()
		draft := BudDraft{Name: name, Role: "Reviewer", Instructions: "Review release notes."}
		if _, _, _, err := s.SaveVoiceBud(ctx, chat, "call", DefaultAgentID, "Draft a Bud named "+name+".", draft, prerequisite, followup); err != nil {
			t.Fatal(err)
		}
		conversation, err := s.Conversation(ctx, chat, 0)
		if err != nil || len(conversation.Messages) == 0 {
			t.Fatalf("missing draft: %+v, %v", conversation, err)
		}
		message := conversation.Messages[len(conversation.Messages)-1]
		if _, err := s.CreateProposedBud(ctx, chat, message.TaskID, CreateBudWithWork); err != nil {
			t.Fatal(err)
		}
	}
	approve("Finch")
	revoked := addCheckoutWorker(t, s, "Revoked worker")
	setWorkerCheckout(t, s, revoked, project.ID, t.TempDir())
	if err := s.RevokeDevice(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	approve("Wren")
	jobs, err := s.WorkOverview(ctx, chat)
	if err != nil || len(jobs) != 3 {
		t.Errorf("expected two reviews and one shared prerequisite: %+v, %v", jobs, err)
	}
	job, err := s.ReserveExecutionWork(ctx, worker, true, true)
	if err != nil || job == nil || job.Title != prerequisite.Title {
		t.Fatalf("worker could not claim the prerequisite: %+v, %v", job, err)
	}
	if err := s.FinishDeviceJob(ctx, worker, job.ID, DeviceResult{Lease: job.Lease, Status: Succeeded}); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ReserveExecutionWork(ctx, worker, true, true); err != nil || job != nil {
		t.Fatalf("worker claimed a duplicate prerequisite: %+v, %v", job, err)
	}
}

func TestQueuedPoolAssignmentReusesEligibleCheckouts(t *testing.T) {
	ctx, s := t.Context(), teamTestStore(t)
	project, err := s.SaveProject(ctx, Project{Name: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	first := addCheckoutWorker(t, s, "Pool worker")
	other := addCheckoutWorker(t, s, "Other worker")
	checkout := t.TempDir()
	setWorkerCheckout(t, s, first, project.ID, checkout, "go")
	if _, err := s.ReserveExecutionWork(ctx, first, true, true); err != nil {
		t.Fatal(err)
	}
	const chat = DefaultConversationID
	handoff := Handoff{AgentID: newBudAssignee, DeviceID: DevicePool, Title: "Edit release notes", Brief: "Update the release notes.",
		ExecutionPlan: ExecutionPlan{ProjectID: project.ID, Requirements: []string{"go"}}}
	draft := BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review release notes."}
	if _, _, _, err := s.SaveVoiceBud(ctx, chat, "call", DefaultAgentID, "Draft a Bud named Finch.", draft, handoff); err != nil {
		t.Fatal(err)
	}
	conversation, err := s.Conversation(ctx, chat, 0)
	if err != nil || len(conversation.Messages) != 1 || conversation.Messages[0].BudProposal == nil {
		t.Fatalf("missing draft: %+v, %v", conversation, err)
	}
	bud, err := s.CreateProposedBud(ctx, chat, conversation.Messages[0].TaskID, CreateBudWithWork)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.WorkOverview(ctx, chat)
	if err != nil || len(jobs) != 1 || jobs[0].Status != Queued {
		t.Fatalf("expected one queued pool job: %+v, %v", jobs, err)
	}
	handoff.AgentID = bud.ID
	repeat := func(key string, reused bool) AssignedWork {
		t.Helper()
		result, err := s.QueueVoiceAssignments(ctx, chat, DefaultAgentID, "call", key, []Handoff{handoff})
		if err != nil || len(result) != 1 || result[0].Reused != reused || (result[0].ID == jobs[0].ID) != reused {
			t.Fatalf("expected reuse=%v for %s: %+v, %v", reused, key, result, err)
		}
		return result[0]
	}
	otherCheckout := t.TempDir()
	setWorkerCheckout(t, s, other, project.ID, otherCheckout, "go")
	repeat("outside-pool", true)
	setWorkerCheckout(t, s, other, project.ID, otherCheckout)
	if job, err := s.ReserveExecutionWork(ctx, other, true, true); err != nil || job != nil {
		t.Fatalf("worker without Go claimed draft work: %+v, %v", job, err)
	}
	repeat("without-required-tool", true)
	setWorkerCheckout(t, s, other, project.ID, otherCheckout, "go")
	newJob := repeat("new-eligible-worker", false)
	if job, err := s.ReserveExecutionWork(ctx, other, true, true); err != nil || job == nil || job.ID != newJob.ID {
		t.Fatalf("newly eligible worker did not claim the unrestricted job: %+v, %v", job, err)
	}
	if job, err := s.ReserveExecutionWork(ctx, first, true, true); err != nil || job == nil || job.ID != jobs[0].ID {
		t.Fatalf("original worker did not claim the draft job: %+v, %v", job, err)
	}
}

func TestBudApprovalPreservesUnrestrictedWorkerAssignments(t *testing.T) {
	for _, route := range []string{"pool", "named worker"} {
		t.Run(route, func(t *testing.T) {
			ctx, s := t.Context(), teamTestStore(t)
			project, err := s.SaveProject(ctx, Project{Name: "Service"})
			if err != nil {
				t.Fatal(err)
			}
			first := addCheckoutWorker(t, s, "Original worker")
			target := first
			if route == "pool" {
				target = DevicePool
			}
			setWorkerCheckout(t, s, first, project.ID, t.TempDir())
			if _, err := s.ReserveExecutionWork(ctx, first, true, true); err != nil {
				t.Fatal(err)
			}
			const chat = DefaultConversationID
			prerequisite := Handoff{AgentID: DefaultAgentID, DeviceID: target, Title: "Collect release notes", Brief: "Collect the draft notes.",
				ExecutionPlan: ExecutionPlan{ProjectID: project.ID}}
			original, err := s.QueueVoiceHandoff(ctx, chat, "earlier-call", "original-job", prerequisite)
			if err != nil {
				t.Fatal(err)
			}
			draft := BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review release notes."}
			followup := Handoff{AgentID: newBudAssignee, Title: "Review release notes", Brief: "Review the collected notes.", After: []int{1}}
			if _, _, _, err := s.SaveVoiceBud(ctx, chat, "call", DefaultAgentID, "Draft a Bud named Finch.", draft, prerequisite, followup); err != nil {
				t.Fatal(err)
			}
			conversation, err := s.Conversation(ctx, chat, 0)
			if err != nil || len(conversation.Messages) == 0 {
				t.Fatalf("missing draft: %+v, %v", conversation, err)
			}
			message := conversation.Messages[len(conversation.Messages)-1]
			if message.BudProposal == nil {
				t.Fatalf("missing draft card: %+v", message)
			}
			bud, err := s.CreateProposedBud(ctx, chat, message.TaskID, CreateBudWithWork)
			if err != nil {
				t.Fatal(err)
			}
			jobs, err := s.WorkOverview(ctx, chat)
			if err != nil || len(jobs) != 3 {
				t.Errorf("approval must queue its own checkout-bound prerequisite: %+v, %v", jobs, err)
			}
			worker := first
			if route == "pool" {
				if err := s.RevokeDevice(ctx, first); err != nil {
					t.Fatal(err)
				}
				worker = addCheckoutWorker(t, s, "Replacement pool worker")
			}
			checkout := t.TempDir()
			setWorkerCheckout(t, s, worker, project.ID, checkout)
			job, err := s.ReserveExecutionWork(ctx, worker, true, true)
			if err != nil || job == nil || job.ID != original.ID || job.Workspace != checkout {
				t.Fatalf("worker could not claim the unrestricted work: %+v, %v", job, err)
			}
			if err := s.FinishDeviceJob(ctx, worker, job.ID, DeviceResult{Lease: job.Lease, Status: Succeeded}); err != nil {
				t.Fatal(err)
			}
			if job, err := s.ReserveExecutionWork(ctx, worker, true, true); err != nil || job != nil {
				t.Fatalf("worker claimed draft work outside the approved checkout: %+v, %v", job, err)
			}
			if job, err := s.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Bud %s started without its approved prerequisite: %+v, %v", bud.Name, job, err)
			}
		})
	}
}

func addCheckoutWorker(t *testing.T, s *Store, name string) string {
	t.Helper()
	device, err := s.EnrollDevice(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return device.ID
}

func setWorkerCheckout(t *testing.T, s *Store, host, project, path string, capabilities ...string) {
	t.Helper()
	profile := fixtureHost(capabilities...)
	profile.Projects = []ProjectBinding{{ProjectID: project, Path: path}}
	if err := s.SaveHostProfile(t.Context(), host, profile); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverHostUsesConfiguredCodexOutsidePATH(t *testing.T) {
	runner := filepath.Join(t.TempDir(), "custom-runner")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewCodexRuntime(runner, "", localconfig.CodexKey{Dir: state})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	profile := runtime.DiscoverHost(t.Context())
	for _, cap := range profile.Capabilities {
		if cap.ID == "codex" {
			if !cap.Installed || !cap.Enabled || !cap.Ready {
				t.Fatalf("configured runner was marked unavailable: %+v", cap)
			}
			return
		}
	}
	t.Fatal("Codex capability is missing")
}

func TestLocalBlockedJobsKeepTheirExplanationAndResume(t *testing.T) {
	for _, blocker := range []string{"disabled", "checkout changed"} {
		t.Run(blocker, func(t *testing.T) {
			ctx, s := t.Context(), teamTestStore(t)
			project, err := s.SaveProject(ctx, Project{Name: "Blocked project"})
			if err != nil {
				t.Fatal(err)
			}
			checkout, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			profile := fixtureHost("browser")
			profile.Projects = []ProjectBinding{{ProjectID: project.ID, Path: checkout}}
			if err := s.SaveHostProfile(ctx, "server", profile); err != nil {
				t.Fatal(err)
			}
			jobs, err := s.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "blocked", []Handoff{{ExecutionPlan: ExecutionPlan{ProjectID: project.ID, Requirements: []string{"browser"}}, Title: "Inspect website", Brief: "Inspect website.", ReadOnly: true}})
			if err != nil {
				t.Fatal(err)
			}
			if blocker == "disabled" {
				profile.Capabilities[0].Enabled = false
			} else {
				profile.Projects[0].Path, err = filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SaveHostProfile(ctx, "server", profile); err != nil {
				t.Fatal(err)
			}
			if _, err := s.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
				t.Fatal("blocked job was claimed", err)
			}
			queued, err := s.Get(ctx, jobs[0].ID)
			if err != nil || queued.Status != Queued || !strings.Contains(queued.Summary, blocker) {
				t.Fatalf("blocker was rolled back: %+v %v", queued, err)
			}
			profile.Capabilities[0].Enabled, profile.Projects[0].Path = true, checkout
			if err := s.SaveHostProfile(ctx, "server", profile); err != nil {
				t.Fatal(err)
			}
			if job, err := s.claimLane(ctx, true); err != nil || job.ID != jobs[0].ID || job.Status != Running {
				t.Fatal("ready job did not resume", job, err)
			}
		})
	}
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
