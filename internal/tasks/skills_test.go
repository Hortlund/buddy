package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestQueuedSkillUsesInstalledVersionAndFollowsHandoff(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	markdown := "---\r\nname: triage-ci\r\ndescription: >\r\n  Identify failed steps\r\n  from CI output.\r\nallowed-tools: Bash\r\n---\r\nVersion one."
	installed, err := store.InstallSkill(ctx, markdown)
	if err != nil || installed.Name != "triage-ci" || installed.Description != "Identify failed steps from CI output." {
		t.Fatalf("import: %+v, %v", installed, err)
	}
	if _, err := store.InstallSkill(ctx, strings.Replace(markdown, "triage-ci", "review", 1)); err == nil {
		t.Fatal("a custom skill replaced a built-in command")
	}
	if _, err := store.InstallSkill(ctx, strings.Replace(markdown, "allowed-tools: Bash", "user-invocable: false", 1)); err == nil {
		t.Fatal("a model-only skill was installed as a slash command")
	}
	text := "/triage-ci compare the last two runs"
	first, _, err := store.Send(ctx, DefaultConversationID, text, "skill-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InstallSkill(ctx, strings.Replace(markdown, "Version one.", "Version two.", 1)); err != nil {
		t.Fatal(err)
	}
	if retry, queued, err := store.Send(ctx, DefaultConversationID, text, "skill-fixture"); err != nil || queued || retry.ID != first.ID {
		t.Fatalf("retry changed the original invocation: %+v, %v, %v", retry, queued, err)
	}
	if claim, err := store.claimLane(ctx, false); err != nil || claim.ID != first.ID {
		t.Fatalf("could not claim skill turn: %+v, %v", claim, err)
	}
	turn, err := store.turn(ctx, first)
	if err != nil || turn.Skill == nil || turn.Skill.Instructions != "Version one." || turn.Skill.Arguments != "compare the last two runs" {
		t.Fatalf("queued invocation was changed: %+v, %v", turn.Skill, err)
	}
	if err := store.finishTurn(ctx, first.ID, Succeeded, "Replied.", "I’ll check the logs.", &TurnAction{
		Handoff: &Handoff{Title: "Check CI", Brief: "Compare the last two runs.", ReadOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Send(ctx, DefaultConversationID, "/triage-ci check today's run", "new-version")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveSkill(ctx, "triage-ci"); err != nil {
		t.Fatal(err)
	}
	worker, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	workerTurn, err := store.turn(ctx, worker)
	if err != nil || workerTurn.Skill == nil || workerTurn.Skill.Instructions != "Version one." || workerTurn.Agent.AllowEdits {
		t.Fatalf("handoff lost the skill or read-only scope: %+v, %v", workerTurn, err)
	}
	newTurn, err := store.turn(ctx, second)
	if err != nil || newTurn.Skill == nil || newTurn.Skill.Instructions != "Version two." {
		t.Fatalf("new invocation did not use the revised skill: %+v, %v", newTurn.Skill, err)
	}
	for _, plain := range []string{"/tmp/logs.txt", "/triage-ci removed skill", "Please quote /review"} {
		message, _, err := store.Send(ctx, DefaultConversationID, plain, plain)
		if err != nil {
			t.Fatal(err)
		}
		turn, err := store.turn(ctx, message)
		if err != nil || turn.Skill != nil {
			t.Fatalf("plain chat was treated as a skill: %q, %+v, %v", plain, turn.Skill, err)
		}
	}
	conversation, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || conversation.Messages[0].SkillName != "triage-ci" {
		t.Fatalf("chat lost the applied command: %+v, %v", conversation.Messages, err)
	}
}

func TestBudDraftRequiresUserActionAndCreatesOneBud(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := t.TempDir()
	if err := store.EnableWorkspaces(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableProfiles(ctx, filepath.Join(t.TempDir(), "profiles")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Send(ctx, DefaultConversationID, "/create-bud Make a CI Bud named Finch", "bud-draft"); err != nil {
		t.Fatal(err)
	}
	job, err := store.claimLane(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, job)
	if err != nil || turn.Skill == nil || turn.Skill.Name != "create-bud" {
		t.Fatalf("creation skill was missing: %+v, %v", turn.Skill, err)
	}
	turn.action = new(TurnAction)
	reply, err := turn.decodeReply(`{"reply":"","bud_proposal":{"name":"Finch","role":"CI specialist","instructions":"Investigate failed builds and report back."}}`, nil)
	if err != nil || turn.action.BudProposal == nil {
		t.Fatalf("model draft was not decoded: %+v, %v", turn.action, err)
	}
	if err := store.finishTurn(ctx, job.ID, Succeeded, "Replied.", reply, turn.action); err != nil {
		t.Fatal(err)
	}
	if dir, err := store.Directory(ctx); err != nil || len(dir.Agents) != 1 {
		t.Fatalf("model draft created a Bud without user action: %+v, %v", dir.Agents, err)
	}
	if chat, err := store.Conversation(ctx, DefaultConversationID, 0); err != nil || len(chat.Messages) != 2 || chat.Messages[1].BudProposal == nil || chat.Messages[1].BudProposal.ConversationID != "" {
		t.Fatalf("missing reviewable draft: %+v, %v", chat.Messages, err)
	}
	if _, err := store.CreateProposedBud(ctx, "conv_other", job.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a different chat could create the draft: %v", err)
	}
	var results [2]Agent
	var outcomes [2]error
	var requests sync.WaitGroup
	for i := range results {
		requests.Go(func() { results[i], outcomes[i] = store.CreateProposedBud(ctx, DefaultConversationID, job.ID) })
	}
	requests.Wait()
	first, second := results[0], results[1]
	if outcomes[0] != nil || outcomes[1] != nil || first.ID == "" || first.ID != second.ID || first.ConversationID != second.ConversationID {
		t.Fatalf("creation was not idempotent: %+v, %v", results, outcomes)
	}
	if info, err := os.Stat(first.Workspace); err != nil || !info.IsDir() || filepath.Dir(first.Workspace) != workspace {
		t.Fatalf("Bud did not receive its managed workspace: %+v, %v", first, err)
	}
	if chat, err := store.Conversation(ctx, first.ConversationID, 0); err != nil || chat.Agent.Role != "CI specialist" {
		t.Fatalf("created chat was unavailable: %+v, %v", chat.Agent, err)
	}
	docs, err := store.BudDocuments(ctx, first.ID)
	if err != nil || !strings.Contains(docs.Soul, "Finch") || !strings.Contains(docs.Soul, "CI specialist") ||
		!strings.Contains(docs.Soul, "Investigate failed builds") {
		t.Fatalf("created Bud did not have a personalized SOUL.md: %+v, %v", docs, err)
	}
	if chat, err := store.Conversation(ctx, DefaultConversationID, 0); err != nil || chat.Messages[1].BudProposal.ConversationID != first.ConversationID {
		t.Fatalf("original card did not update: %+v, %v", chat.Messages, err)
	}
}

func TestReviewSkillStaysReadOnlyAcrossChoiceAndDelegation(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	reviewer, err := store.CreateAgent(ctx, Agent{Name: "Finch"})
	if err != nil || !reviewer.AllowEdits {
		t.Fatalf("reviewer fixture should normally allow edits: %+v, %v", reviewer, err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Review team", Members: []string{DefaultAgentID, reviewer.ID}})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.Send(ctx, group.ID, "/review the auth changes", "review-request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, first.ID, Succeeded, "Replied.", "", &TurnAction{
		Card: &ChoiceCard{Question: "Which scope?", Options: []string{"Latest diff", "Whole branch"}},
	}); err != nil {
		t.Fatal(err)
	}
	answer, _, err := store.AnswerCard(ctx, group.ID, first.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, answer)
	if err != nil || turn.Skill == nil || turn.Skill.Name != "review" || turn.Skill.Arguments != "the auth changes" || turn.Agent.AllowEdits {
		t.Fatalf("answer lost the review scope: %+v, %v", turn, err)
	}
	if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "I’ll hand that off.", &TurnAction{
		Handoff: &Handoff{AgentID: reviewer.ID, Title: "Review auth", Brief: "Review the latest diff."},
	}); err != nil {
		t.Fatal(err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.turn(ctx, job)
	if err != nil || turn.Skill == nil || turn.Agent.ID != reviewer.ID || turn.Agent.AllowEdits {
		t.Fatalf("delegated review gained write access or lost its skill: %+v, %v", turn, err)
	}
}

func TestDeviceClaimsSkillWorkOnlyWhenItCanUseTheSkill(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	device, err := store.EnrollDevice(ctx, "Remote worker")
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := store.Send(ctx, DefaultConversationID, "/brief compare these logs", "device-skill")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "I’ll send this to the worker.", &TurnAction{
		Handoff: &Handoff{DeviceID: device.ID, Title: "Compare logs", Brief: "Summarize the log differences.", ReadOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	if legacy, err := store.ReserveDeviceWork(ctx, device.ID, false, false); err != nil || legacy != nil {
		t.Fatalf("old worker took a skill job: %+v, %v", legacy, err)
	}
	job, err := store.ReserveDeviceSkillWork(ctx, device.ID, false, false)
	if err != nil || job == nil || !job.NeedsStart || job.Skill == nil || job.Skill.Name != "brief" || job.Skill.Arguments != "compare these logs" {
		t.Fatalf("worker did not receive the bound skill: %+v, %v", job, err)
	}
	if saved, err := store.Get(ctx, job.ID); err != nil || saved.Status != Queued {
		t.Fatalf("skill job started before delivery was acknowledged: %+v, %v", saved, err)
	}
	if err := store.StartDeviceJob(ctx, device.ID, job.ID, job.Lease); err != nil {
		t.Fatal(err)
	}
}

func TestReviewRoutineKeepsSkillAndReadOnlyScope(t *testing.T) {
	for _, destination := range []string{"server", "device"} {
		t.Run(destination, func(t *testing.T) {
			ctx := t.Context()
			store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			agent, err := store.Agent(ctx, DefaultAgentID)
			if err != nil || !agent.AllowEdits {
				t.Fatalf("expected an editable Bud workspace: %+v, %v", agent, err)
			}
			var device string
			if destination == "device" {
				worker, err := store.EnrollDevice(ctx, "Remote reviewer")
				if err != nil {
					t.Fatal(err)
				}
				device = worker.ID
			}
			const arguments = "review the latest diff daily"
			first, _, err := store.Send(ctx, DefaultConversationID, "/review "+arguments, "schedule-review")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.claimLane(ctx, false); err != nil {
				t.Fatal(err)
			}
			if err := store.finishTurn(ctx, first.ID, Succeeded, "Replied.", "", &TurnAction{
				Routine: &RoutineDraft{DeviceID: device, Name: "Daily review", Prompt: "Review the latest diff.", Minutes: 1440},
			}); err != nil {
				t.Fatal(err)
			}
			chat, err := store.Conversation(ctx, DefaultConversationID, 0)
			if err != nil || len(chat.Routines) != 1 {
				t.Fatalf("review was not scheduled: %+v, %v", chat.Routines, err)
			}
			if queued, err := store.enqueueDueRoutine(ctx, chat.Routines[0].NextAt); err != nil || !queued {
				t.Fatalf("review routine did not enqueue: %v, %v", queued, err)
			}
			var skill *SkillInvocation
			var allowEdits bool
			if device == "" {
				job, err := store.claimLane(ctx, true)
				if err != nil || job.ID == "" {
					t.Fatalf("routine job did not reach the server worker: %+v, %v", job, err)
				}
				turn, err := store.turn(ctx, job)
				if err != nil {
					t.Fatal(err)
				}
				skill, allowEdits = turn.Skill, turn.Agent.AllowEdits
			} else {
				if job, err := store.ReserveDeviceWork(ctx, device, false, true); err != nil || job != nil {
					t.Fatalf("old worker accepted a routine with a skill: %+v, %v", job, err)
				}
				job, err := store.ReserveDeviceSkillWork(ctx, device, false, true)
				if err != nil || job == nil {
					t.Fatalf("routine job did not reach the paired worker: %+v, %v", job, err)
				}
				skill, allowEdits = job.Skill, job.Agent.AllowEdits
			}
			if skill == nil || skill.Name != "review" || skill.Arguments != arguments || !strings.Contains(skill.Instructions, "Keep the review read-only.") || allowEdits {
				t.Fatalf("routine lost its review skill or gained write access: skill=%+v allowEdits=%v", skill, allowEdits)
			}
		})
	}
}
