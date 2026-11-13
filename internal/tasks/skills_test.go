package tasks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"buddy/internal/localconfig"
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
	if _, err := store.CreateProposedBud(ctx, "conv_other", job.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a different chat could create the draft: %v", err)
	}
	var results [2]Agent
	var outcomes [2]error
	var requests sync.WaitGroup
	for i := range results {
		requests.Go(func() { results[i], outcomes[i] = store.CreateProposedBud(ctx, DefaultConversationID, job.ID, "") })
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

func TestBudCreationChecksCurrentRequestInChatAndVoice(t *testing.T) {
	for _, tc := range []struct {
		name, request, evidence string
		create                  bool
	}{
		{"direct", "Create a Bud named Finch.", "Create a Bud named Finch.", true},
		{"polite", "Can you please create a new Bud for CI?", "Can you please create a new Bud for CI?", true},
		{"purpose timing", "Create a Bud named Finch to check CI after each merge.", "Create a Bud named Finch", true},
		{"polite named", "Create a Bud named Finch, please.", "Create a Bud named Finch", true},
		{"addressed", "Hey Buddy, create a Bud named Finch and assign Finch to review the logs.", "create a Bud named Finch", true},
		{"swedish", "Kan du skapa en ny Bud som heter Finch?", "Kan du skapa en ny Bud som heter Finch?", true},
		{"quote", `"Create a Bud named Finch."`, "Create a Bud named Finch.", false},
		{"single quote", "'Create a Bud named Finch.'", "Create a Bud named Finch.", false},
		{"example", "What would happen if I said create a Bud named Finch?", "create a Bud named Finch", false},
		{"negated", "Do not create a Bud named Finch.", "create a Bud named Finch.", false},
		{"conditional", "Create a Bud named Finch if I say yes later.", "Create a Bud named Finch", false},
		{"preview", "Create a Bud for CI, only as a draft.", "Create a Bud for CI", false},
		{"show the draft first", "Create a Bud named Finch for CI and show me the draft first.", "Create a Bud named Finch", false},
		{"show its draft first", "Create a Bud named Finch for CI and show me its draft first.", "Create a Bud named Finch", false},
		{"keep the draft", "Create a Bud named Finch for CI and keep the draft in chat for review.", "Create a Bud named Finch", false},
		{"keep a proposal", "Create a Bud named Finch for CI and keep it as a proposal for now.", "Create a Bud named Finch", false},
		{"draft mode", "Create a Bud named Finch for CI and keep it in draft mode.", "Create a Bud named Finch", false},
		{"ask for sign-off", "Create a Bud named Finch, ask me to sign off on the profile first.", "Create a Bud named Finch", false},
		{"approval after another clause", "Create a Bud named Finch, have Forge pull the build logs, then ask me for the go-ahead.", "Create a Bud named Finch", false},
		{"decision after another clause", "Create a Bud named Finch and run a CI check, then let me decide whether to proceed.", "Create a Bud named Finch", false},
		{"group mention", "@Buddy: Create a Bud named Finch.", "@Buddy: Create a Bud named Finch.", true},
		{"unrecognized preview clause", "Create a Bud named Finch for CI and only sketch its profile in chat.", "Create a Bud named Finch", false},
		{"comma before follow-up", "Create a Bud named Finch, have Finch check CI, then have another Bud summarize.", "Create a Bud named Finch", true},
		{"report the outcome", "Create a Bud named Finch and have Finch review the logs, then let me know.", "Create a Bud named Finch", true},
		{"report on completion", "Create a Bud named Finch and have Finch review the logs, then let me know when it's done.", "Create a Bud named Finch", true},
		{"typographic apostrophe", "Create a Bud named Finch and have Finch review the logs, then let me know when it’s done.", "Create a Bud named Finch", true},
		{"curly single quote", "‘Create a Bud named Finch.’", "Create a Bud named Finch.", false},
		{"preview after a dash", "Create a Bud named Finch for CI — show me the draft first.", "Create a Bud named Finch", false},
		{"cancelled", "Create a Bud for CI. Cancel that.", "Create a Bud for CI", false},
		{"qualified", "Create a Bud for CI. Wait until next week.", "Create a Bud for CI", false},
		{"deferred approval", "Create a Bud named Finch for CI after I approve the draft.", "Create a Bud named Finch", false},
		{"subject to approval", "Create a Bud named Finch for CI, subject to my approval.", "Create a Bud named Finch", false},
		{"discuss first", "Create a Bud named Finch for CI, but let's discuss the draft first.", "Create a Bud named Finch", false},
		{"approval without punctuation", "Create a Bud named Finch for CI subject to my approval.", "Create a Bud named Finch", false},
		{"following sign-off", "Create a Bud named Finch for CI following sign-off.", "Create a Bud named Finch", false},
		{"on sign-off", "Create a Bud named Finch for CI on final sign off.", "Create a Bud named Finch", false},
		{"as soon as approved", "Create a Bud named Finch as soon as I approve.", "Create a Bud named Finch", false},
		{"typographic retraction", "Create a Bud named Finch and run CI. Don’t create it yet.", "Create a Bud named Finch", false},
		{"deferred event", "Create a Bud named Finch for CI when the current build finishes.", "Create a Bud named Finch", false},
		{"deferred compound", "Create a Bud named Finch and assign Finch to review the logs. Wait until I confirm.", "Create a Bud named Finch", false},
		{"retracted", "Create a Bud named Finch. Actually, wait.", "Create a Bud named Finch.", false},
		{"different name", "Create a Bud named Raven.", "Create a Bud named Raven.", false},
		{"unverified evidence", "Create a Bud named Finch.", "Create a new Bud named Finch.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			draft := BudDraft{Name: "Finch", Role: "CI reviewer", Instructions: "Review CI results.", CreateNow: true, Evidence: tc.evidence}
			turn, _, err := store.Send(ctx, DefaultConversationID, tc.request, "chat-create")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.claimLane(ctx, false); err != nil {
				t.Fatal(err)
			}
			if err := store.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "", &TurnAction{BudProposal: &draft}); err != nil {
				t.Fatal(err)
			}
			voiceBud, _, _, err := store.SaveVoiceBud(ctx, DefaultConversationID, "call", DefaultAgentID, tc.request, draft)
			if err != nil || (voiceBud.ID != "") != tc.create {
				t.Fatalf("voice creation = %+v, %v, wanted creation %v", voiceBud, err, tc.create)
			}
			chat, err := store.Conversation(ctx, DefaultConversationID, 0)
			if err != nil || len(chat.Messages) != 3 {
				t.Fatalf("missing receipts: %+v, %v", chat.Messages, err)
			}
			for _, message := range chat.Messages[1:] {
				if message.BudProposal == nil || (message.BudProposal.ConversationID != "") != tc.create {
					t.Fatalf("wrong receipt for %s: %+v", message.Origin, message)
				}
			}
		})
	}
}

func TestBudProvisionFailureKeepsDraftAndIndependentAssignment(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	store, err := OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := filepath.Join(dir, "workspaces")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableWorkspaces(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, nil, 0600); err != nil {
		t.Fatal(err)
	}
	request := "Create a Bud named Finch and queue a release review."
	turn, _, err := store.Send(ctx, DefaultConversationID, request, "retryable-bud")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	draft := BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review release changes.", CreateNow: true, Evidence: request}
	action := &TurnAction{BudProposal: &draft, Handoff: &Handoff{Title: "Release review", Brief: "Review the release notes."}}
	if err := store.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "", action); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 2 || len(chat.Work) != 1 {
		t.Fatalf("failed creation stalled the reply or assignment: %+v, %v", chat, err)
	}
	message := chat.Messages[1]
	if message.BudProposal == nil || message.BudProposal.ConversationID != "" || !strings.Contains(message.Text, budProvisionNotice) {
		t.Fatalf("missing retryable draft: %+v", message)
	}
	if bud, _, notice, err := store.SaveVoiceBud(ctx, DefaultConversationID, "call", DefaultAgentID, request, draft); err != nil || bud.ID != "" || notice != budProvisionNotice {
		t.Fatalf("voice did not save a retryable draft: %+v, %q, %v", bud, notice, err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root+"-offline", root); err != nil {
		t.Fatal(err)
	}
	bud, err := store.CreateProposedBud(ctx, DefaultConversationID, turn.ID, "")
	if err != nil || bud.ConversationID == "" {
		t.Fatalf("could not retry the saved draft: %+v, %v", bud, err)
	}
}

func TestBudDraftUsesRevisedProfileAfterAnsweringChoice(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	const request = "Create a Bud named Finch for CI and assign it to review failed builds. Ask which checkout first."
	turn, _, err := store.Send(ctx, DefaultConversationID, request, "bud-with-choice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	action := TurnAction{
		BudProposal:      &BudDraft{Name: "Finch", Role: "CI reviewer", Instructions: "Review failed builds.", CreateNow: true, Evidence: "Create a Bud named Finch"},
		Card:             &ChoiceCard{Question: "Which checkout?", Options: []string{"Stable", "Beta"}},
		BudClarification: true,
		Handoffs:         []Handoff{{AgentID: newBudAssignee, Title: "Review builds", Brief: "Review failed builds."}},
	}
	if err := store.finishTurn(ctx, turn.ID, Succeeded, "Replied.", "", &action); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 2 || chat.Messages[1].Card == nil || chat.Messages[1].BudProposal == nil || chat.Messages[1].BudProposal.ConversationID != "" {
		t.Fatalf("missing choice and draft: %+v, %v", chat.Messages, err)
	}
	if _, err := store.CreateProposedBud(ctx, DefaultConversationID, turn.ID, ""); err == nil {
		t.Fatal("created the draft before the choice was answered")
	}
	answer, _, err := store.AnswerCard(ctx, DefaultConversationID, turn.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProposedBud(ctx, DefaultConversationID, turn.ID, ""); err == nil {
		t.Fatal("the original draft was unlocked before the choice was applied")
	}
	for _, incomplete := range []*TurnAction{
		{},
		{BudProposal: &BudDraft{Name: "Finch"}},
		{BudProposal: action.BudProposal, Card: &ChoiceCard{Question: "Which branch?", Options: []string{"Beta"}}},
	} {
		if _, err := store.claimLane(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "I'll update the draft.", incomplete); err != nil {
			t.Fatal(err)
		}
		finished, err := store.Get(ctx, answer.ID)
		if err != nil || finished.Status != Failed {
			t.Fatalf("incomplete revision closed the choice: %+v, %v", finished, err)
		}
		chat, err := store.Conversation(ctx, DefaultConversationID, 0)
		if err != nil || chat.Messages[1].Card == nil || chat.Messages[1].Card.Choice != nil {
			t.Fatalf("choice did not reopen: %+v, %v", chat.Messages, err)
		}
		retry, created, err := store.AnswerCard(ctx, DefaultConversationID, turn.ID, 1)
		if err != nil || !created || retry.ID == answer.ID {
			t.Fatalf("could not retry the revision: %+v, %v", retry, err)
		}
		answer = retry
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	followup, err := store.turn(ctx, answer)
	if err != nil || !slices.ContainsFunc(followup.Messages, func(m Message) bool {
		return m.TaskID == turn.ID && m.BudProposal != nil && m.BudProposal.Name == "Finch" &&
			m.BudProposal.Role == "CI reviewer" && m.BudProposal.Instructions == "Review failed builds."
	}) {
		t.Fatalf("missing the original Bud profile: %+v, %v", followup.Messages, err)
	}
	const revised = "Review failed builds in the Beta checkout."
	fixture := t.TempDir()
	if err := os.Chmod(fixture, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", fixture)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("BUDDY_OPENAI_API_KEY", "")
	cli := filepath.Join(fixture, "codex")
	payload, err := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]string{
			"type": "agent_message",
			"text": `{"reply":"","bud_proposal":{"name":"Finch","role":"CI reviewer","instructions":"` + revised + `","create_now":false,"evidence":""}}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n/usr/bin/grep -Fq '\"bud_proposal\":{\"name\":\"Finch\",\"role\":\"CI reviewer\",\"instructions\":\"Review failed builds.\",\"needs_revision\":true,\"pending_work\":[\"Review builds\"]}' || exit 1\nprintf '%s\\n' '" + string(payload) + "'\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewCodexRuntime(cli, "", localconfig.CodexKey{Dir: fixture})
	if err != nil {
		t.Fatal(err)
	}
	followup.action = new(TurnAction)
	reply, err := runner.Run(ctx, followup, func(string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", reply, followup.action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProposedBud(ctx, DefaultConversationID, turn.ID, ""); err == nil {
		t.Fatal("the obsolete draft remained creatable")
	}
	if bud, err := store.CreateProposedBud(ctx, DefaultConversationID, answer.ID, CreateBudWithWork); err != nil || bud.Name != "Finch" ||
		bud.ConversationID == "" || bud.Instructions != revised {
		t.Fatalf("couldn't create the revised Bud: %+v, %v", bud, err)
	}
	jobs, err := store.WorkOverview(ctx, DefaultConversationID)
	if err != nil || len(jobs) != 1 || jobs[0].Title != "Review builds" {
		t.Fatalf("revision lost the assignment saved with the original draft: %+v, %v", jobs, err)
	}
}

func TestBudCreationContinuesThroughChoiceOnlyQuestions(t *testing.T) {
	for _, mode := range []string{"direct", "marked", "create-bud skill"} {
		t.Run(mode, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			request := MessageRequest{Text: "@Buddy: Create a Bud named Finch."}
			if mode == "marked" {
				request.Text = "I'd like a new teammate called Finch."
			} else if mode == "create-bud skill" {
				request = MessageRequest{Text: "/create-bud Finch", SkillName: "create-bud"}
			}
			chat := DefaultConversationID
			question, _, err := store.SendMessage(ctx, chat, "bud-without-draft", request)
			if err != nil {
				t.Fatal(err)
			}
			claim := func() Task {
				t.Helper()
				task, err := store.claimLane(ctx, false)
				if err != nil {
					t.Fatal(err)
				}
				return task
			}
			claim()
			card := &ChoiceCard{Question: "Which branch?", Options: []string{"Stable", "Beta"}}
			if err := store.finishTurn(ctx, question.ID, Succeeded, "Replied.", "", &TurnAction{Card: card, BudClarification: true,
				Handoffs: []Handoff{{AgentID: newBudAssignee, Title: "Review builds", Brief: "Review failed builds."}}}); err != nil {
				t.Fatal(err)
			}
			answer, _, err := store.AnswerCard(ctx, chat, question.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := store.turn(ctx, claim())
			if err != nil || !turn.revisingBud {
				t.Fatalf("choice lost its Bud intent: %v", err)
			}
			turn.action = new(TurnAction)
			reply, err := turn.decodeReply(`{"reply":"I'll update the draft."}`, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", reply, turn.action); err != nil {
				t.Fatal(err)
			}
			if finished, err := store.Get(ctx, answer.ID); err != nil || finished.Status != Failed {
				t.Fatalf("text-only response closed the choice: %+v, %v", finished, err)
			}
			retry, created, err := store.AnswerCard(ctx, chat, question.ID, 1)
			if err != nil || !created || retry.ID == answer.ID {
				t.Fatalf("could not retry choice: %+v, %v", retry, err)
			}
			claim()
			second := &ChoiceCard{Question: "Which role?", Options: []string{"Reviewer", "Builder"}}
			if err := store.finishTurn(ctx, retry.ID, Succeeded, "Replied.", "", &TurnAction{Card: second,
				Handoffs: []Handoff{{AgentID: newBudAssignee, Title: "Review Beta", Brief: "Review failed builds on Beta."}}}); err != nil {
				t.Fatal(err)
			}
			last, _, err := store.AnswerCard(ctx, chat, retry.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			turn, err = store.turn(ctx, claim())
			if err != nil || !turn.revisingBud {
				t.Fatalf("second choice lost its Bud intent: %v", err)
			}
			draft := &BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review failed builds on Beta.", CreateNow: true}
			if err := store.finishTurn(ctx, last.ID, Succeeded, "Replied.", "", &TurnAction{BudProposal: draft}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := store.Conversation(ctx, chat, 0)
			if err != nil {
				t.Fatal(err)
			}
			if saved := snapshot.Messages[len(snapshot.Messages)-1].BudProposal; saved == nil || saved.ConversationID != "" ||
				!slices.Equal(saved.PendingWork, []string{"Review Beta"}) {
				t.Fatalf("expected a draft awaiting creation: %+v", saved)
			}
			if bud, err := store.CreateProposedBud(ctx, chat, last.ID, CreateBudWithWork); err != nil || bud.Name != draft.Name || bud.Instructions != draft.Instructions {
				t.Fatalf("could not create completed draft: %+v, %v", bud, err)
			}
		})
	}
}

func TestPreviewChoiceDoesNotAuthorizeBudCreation(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	request, _, err := store.Send(ctx, DefaultConversationID, "Let me pick an action to preview.", "preview-choice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	const label = "Create a Bud named Finch"
	card := &ChoiceCard{Question: "Which action should I preview?", Options: []string{label, "Schedule CI"}}
	if err := store.finishTurn(ctx, request.ID, Succeeded, "Replied.", "", &TurnAction{Card: card}); err != nil {
		t.Fatal(err)
	}
	answer, _, err := store.AnswerCard(ctx, DefaultConversationID, request.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	draft := &BudDraft{Name: "Finch", Role: "CI reviewer", Instructions: "Review CI.", CreateNow: true, Evidence: label}
	if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", &TurnAction{BudProposal: draft}); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 4 || chat.Messages[3].BudProposal == nil ||
		chat.Messages[3].BudProposal.ConversationID != "" {
		t.Fatalf("expected a draft after choosing an action to preview: %+v, %v", chat.Messages, err)
	}
}

func TestJobChoiceKeepsItsPurposeInMixedBudRequest(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "chat"
		if background {
			name = "background planner"
		}
		t.Run(name, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			chat := DefaultConversationID
			const text = "Create a Bud named Finch for CI. Separately, run a release audit and ask which environment to use."
			request, _, err := store.SendMessage(ctx, chat, "bud-and-audit", MessageRequest{Text: text, Background: background})
			if err != nil {
				t.Fatal(err)
			}
			finish := func(id, response string) {
				t.Helper()
				claimed, err := store.claimLane(ctx, false)
				if err != nil || claimed.ID != id {
					t.Fatalf("claim %s: %+v, %v", id, claimed, err)
				}
				turn, err := store.turn(ctx, claimed)
				if err != nil || turn.revisingBud || (turn.Origin == backgroundRequest) != background {
					t.Fatalf("job question entered the wrong flow: %s, %t, %v", turn.Origin, turn.revisingBud, err)
				}
				turn.action = new(TurnAction)
				reply, err := turn.decodeReply(response, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.finishTurn(ctx, id, Succeeded, "Replied.", reply, turn.action); err != nil {
					t.Fatal(err)
				}
			}
			finish(request.ID, `{"reply":"","bud_proposal":{"name":"Finch","role":"CI reviewer","instructions":"Review CI.","create_now":false},"bud_clarification":false,"card":{"question":"Which environment?","options":["Beta","Stable"]}}`)
			if !background {
				snapshot, err := store.Conversation(ctx, chat, 0)
				if err != nil {
					t.Fatal(err)
				}
				proposal := snapshot.Messages[1].BudProposal
				if proposal == nil || proposal.NeedsRevision == nil || *proposal.NeedsRevision {
					t.Fatalf("unrelated job choice blocked the Bud draft: %+v", proposal)
				}
				if _, err := store.CreateProposedBud(ctx, chat, request.ID, ""); err != nil {
					t.Fatal(err)
				}
			}
			answer, _, err := store.AnswerCard(ctx, chat, request.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			finish(answer.ID, `{"reply":"","handoffs":[{"title":"Release audit","brief":"Audit the beta release.","read_only":true}]}`)
			jobs, err := store.WorkOverview(ctx, chat)
			if err != nil || len(jobs) != 1 || jobs[0].Title != "Release audit" {
				t.Fatalf("expected the chosen job to queue: %+v, %v", jobs, err)
			}
		})
	}
}

func TestUpgradeReopensLegacyBudChoiceChainWithTextOnlyAnswer(t *testing.T) {
	ctx := t.Context()
	store := teamTestStore(t)
	chat := DefaultConversationID
	request, _, err := store.Send(ctx, chat, "@Buddy: Create a Bud named Finch.", "legacy-bud-choice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	card := &ChoiceCard{Question: "Which role?", Options: []string{"Reviewer", "Builder"}}
	if err := store.finishTurn(ctx, request.ID, Succeeded, "Replied.", "", &TurnAction{Card: card}); err != nil {
		t.Fatal(err)
	}
	old, _, err := store.AnswerCard(ctx, chat, request.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	next := &ChoiceCard{Question: "Which branch?", Options: []string{"Beta", "Stable"}}
	if err := store.finishTurn(ctx, old.ID, Succeeded, "Replied.", "", &TurnAction{Card: next}); err != nil {
		t.Fatal(err)
	}
	last, _, err := store.AnswerCard(ctx, chat, old.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, last.ID, Succeeded, "Replied.", "I'll update the draft.", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "DROP TABLE bud_choice_cards; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(ctx, store.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	snapshot, err := upgraded.Conversation(ctx, chat, 0)
	if err != nil || len(snapshot.Messages) < 4 || snapshot.Messages[1].Card == nil || snapshot.Messages[1].Card.Choice == nil ||
		snapshot.Messages[3].Card == nil || snapshot.Messages[3].Card.Choice != nil {
		t.Fatalf("upgrade did not reopen the final question only: %+v, %v", snapshot.Messages, err)
	}
	retry, created, err := upgraded.AnswerCard(ctx, chat, old.ID, 1)
	if err != nil || !created || retry.ID == last.ID {
		t.Fatalf("could not retry the legacy choice: %+v, %v", retry, err)
	}
	claimed, err := upgraded.claimLane(ctx, false)
	if err != nil || claimed.ID != retry.ID {
		t.Fatalf("claim retried choice: %+v, %v", claimed, err)
	}
	turn, err := upgraded.turn(ctx, claimed)
	if err != nil || !turn.revisingBud {
		t.Fatalf("legacy choice did not resume the Bud draft: %v", err)
	}
}

func TestChatCanAssignWorkToBudCreatedInSameTurn(t *testing.T) {
	for _, create := range []bool{true, false} {
		name := "creates and assigns"
		if !create {
			name = "keeps independent work when creation needs approval"
		}
		t.Run(name, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			request := "Create a Bud named Finch and assign Finch to review the logs, then let me know. Also check the build yourself."
			if !create {
				request += " Show me the draft first."
			}
			task, _, err := store.Send(ctx, DefaultConversationID, request, "create-and-assign")
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := store.claimLane(ctx, false)
			if err != nil || claimed.ID != task.ID {
				t.Fatalf("claim chat turn: %+v, %v", claimed, err)
			}
			turn, err := store.turn(ctx, claimed)
			if err != nil {
				t.Fatal(err)
			}
			turn.action = new(TurnAction)
			reply, err := turn.decodeReply(`{"reply":"","bud_proposal":{"name":"Finch","role":"CI reviewer","instructions":"Review logs.","create_now":true,"evidence":"Create a Bud named Finch"},"handoffs":[{"agent_id":"new_bud","title":"Release logs","brief":"Review the logs."},{"title":"Follow up","brief":"Summarize the log review.","after":[1]},{"title":"Check build","brief":"Check the build."}]}`, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", reply, turn.action); err != nil {
				t.Fatal(err)
			}
			chat, err := store.Conversation(ctx, DefaultConversationID, 0)
			if err != nil {
				t.Fatal(err)
			}
			proposal := chat.Messages[len(chat.Messages)-1].BudProposal
			if proposal == nil || (proposal.ConversationID != "") != create {
				t.Fatalf("unexpected Bud proposal: %+v", proposal)
			}
			if !create {
				if len(chat.Work) != 1 || chat.Work[0].Title != "Check build" {
					t.Fatalf("new Bud's work was queued or the independent job was lost: %+v", chat.Work)
				}
				if !slices.Equal(proposal.PendingWork, []string{"Release logs", "Follow up"}) {
					t.Fatalf("deferred jobs missing from proposal: %+v", proposal)
				}
				bud, err := store.CreateProposedBud(ctx, chat.ID, task.ID, CreateBudWithWork)
				if err != nil {
					t.Fatal(err)
				}
				if replay, err := store.CreateProposedBud(ctx, chat.ID, task.ID, CreateBudWithWork); err != nil || replay.ID != bud.ID {
					t.Fatalf("approval was not idempotent: %+v, %v", replay, err)
				}
				proposal.ConversationID = bud.ConversationID
				chat, err = store.Conversation(ctx, chat.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
			}
			budChat, err := store.Conversation(ctx, proposal.ConversationID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(chat.Work) != 3 {
				t.Fatalf("expected three jobs: %+v", chat.Work)
			}
			for _, job := range chat.Work {
				want := DefaultAgentID
				if job.Title == "Release logs" {
					want = budChat.Agent.ID
				}
				if job.AgentID != want {
					t.Fatalf("%s went to %s, want %s", job.Title, job.AgentID, want)
				}
				if job.Title == "Follow up" {
					deps, err := workDependencies(ctx, store.db, job.ID)
					if err != nil || len(deps) != 1 || deps[0].Title != "Release logs" {
						t.Fatalf("assignment dependency was lost: %+v, %v", deps, err)
					}
				}
			}
		})
	}
}

func TestBudWorkApprovalRechecksWorkspaceAndCanCreateOnly(t *testing.T) {
	for _, mode := range []string{"edits disabled", "folder changed", "device removed", "profile unavailable"} {
		t.Run(mode, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			chat := DefaultConversationID
			agent, err := store.Agent(ctx, DefaultAgentID)
			if err != nil {
				t.Fatal(err)
			}
			agent.AllowEdits = true
			agent, err = store.SaveAgent(ctx, agent.ID, agent)
			if err != nil {
				t.Fatal(err)
			}
			device, err := store.EnrollDevice(ctx, "Build Mac")
			if err != nil {
				t.Fatal(err)
			}
			const request = "Create a Bud named Finch and have Finch review the logs. Show me the draft first."
			task, _, err := store.Send(ctx, chat, request, "draft-work")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.claimLane(ctx, false); err != nil {
				t.Fatal(err)
			}
			handoffs := []Handoff{{AgentID: newBudAssignee, Title: "Review logs", Brief: "Review the logs."},
				{Title: "Report findings", Brief: "Report the review.", After: []int{1}}}
			if mode == "device removed" {
				handoffs[0].DeviceID = device.ID
			}
			if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", "", &TurnAction{
				BudProposal: &BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review the logs."}, Handoffs: handoffs,
			}); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "edits disabled", "folder changed":
				agent.AllowEdits = false
				if mode == "folder changed" {
					agent.Workspace = t.TempDir()
				}
				if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
					t.Fatal(err)
				}
			case "device removed":
				if err := store.RevokeDevice(ctx, device.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.EnableProfiles(ctx, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			var draftID string
			if err := store.db.QueryRowContext(ctx, "SELECT bud_id FROM bud_proposals WHERE task_id=?", task.ID).Scan(&draftID); err != nil {
				t.Fatal(err)
			}
			blockedProfile := filepath.Join(store.profilesRoot, draftID)
			if mode == "profile unavailable" {
				if err := os.WriteFile(blockedProfile, []byte("existing file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			bud, err := store.CreateProposedBud(ctx, chat, task.ID, CreateBudWithWork)
			jobs, listErr := store.WorkOverview(ctx, chat)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if mode != "edits disabled" {
				if err == nil || bud.ID != "" || len(jobs) != 0 {
					t.Fatalf("failed approval partially created work: Bud %s, jobs %+v, error %v", bud.ID, jobs, err)
				}
				if mode == "profile unavailable" {
					if data, err := os.ReadFile(blockedProfile); err != nil || string(data) != "existing file" {
						t.Fatalf("provisioning modified an existing file: %q, %v", data, err)
					}
					if err := os.Remove(blockedProfile); err != nil {
						t.Fatal(err)
					}
				}
				for _, root := range []string{store.workspaceRoot, store.profilesRoot} {
					if _, err := os.Lstat(filepath.Join(root, draftID)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed approval left files in %s: %v", root, err)
					}
				}
				bud, err = store.CreateProposedBud(ctx, chat, task.ID, CreateBudOnly)
				if err != nil || bud.ID == "" {
					t.Fatalf("could not create the Bud separately: %+v, %v", bud, err)
				}
				if _, err := store.CreateProposedBud(ctx, chat, task.ID, CreateBudWithWork); err == nil {
					t.Fatal("a stale approval changed the user's create-only choice")
				}
				return
			}
			if err != nil || len(jobs) != 2 {
				t.Fatalf("valid approval did not queue the work: %+v, %v", jobs, err)
			}
			var conflict *BudApprovalConflict
			if _, err := store.CreateProposedBud(ctx, chat, task.ID, CreateBudOnly); !errors.As(err, &conflict) || conflict.Previous != CreateBudWithWork {
				t.Fatalf("stale create-only request lost the recorded outcome: %v", err)
			}
			for _, job := range jobs {
				if job.AgentID == agent.ID {
					var edits bool
					if err := store.db.QueryRowContext(ctx, "SELECT allow_edits FROM chat_turns WHERE task_id=?", job.ID).Scan(&edits); err != nil || edits {
						t.Fatalf("saved assignment kept its revoked permission: %v, %v", edits, err)
					}
				}
			}
		})
	}
}

func TestBudWorkApprovalKeepsDraftScope(t *testing.T) {
	for _, origin := range []string{"voice", "revised chat"} {
		for _, change := range []string{"new folder", "enabled edits"} {
			t.Run(origin+"/"+change, func(t *testing.T) {
				ctx, store := t.Context(), teamTestStore(t)
				const chat = DefaultConversationID
				agent, err := store.Agent(ctx, DefaultAgentID)
				if err != nil {
					t.Fatal(err)
				}
				agent.AllowEdits = false
				agent, err = store.SaveAgent(ctx, agent.ID, agent)
				if err != nil {
					t.Fatal(err)
				}
				original := agent.Workspace
				changeSettings := func() {
					t.Helper()
					agent.AllowEdits = true
					if change == "new folder" {
						agent.Workspace = t.TempDir()
					}
					if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
						t.Fatal(err)
					}
				}
				const request = "Create a Bud named Finch, have Buddy fetch the logs, then have Finch review them. Show me the draft first."
				draft := BudDraft{Name: "Finch", Role: "Reviewer", Instructions: "Review logs."}
				jobs := []Handoff{{Title: "Fetch logs", Brief: "Fetch the logs from your current folder."},
					{AgentID: newBudAssignee, Title: "Review logs", Brief: "Review the collected logs.", After: []int{1}}}
				var task string
				if origin == "voice" {
					if _, _, _, err := store.SaveVoiceBud(ctx, chat, "call", agent.ID, request, draft, jobs...); err != nil {
						t.Fatal(err)
					}
					current, err := store.Conversation(ctx, chat, 0)
					if err != nil || len(current.Messages) == 0 {
						t.Fatalf("voice proposal unavailable: %v", err)
					}
					task = current.Messages[len(current.Messages)-1].TaskID
					changeSettings()
				} else {
					first, _, err := store.Send(ctx, chat, request, "initial-request")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.claimLane(ctx, false); err != nil {
						t.Fatal(err)
					}
					card := &ChoiceCard{Question: "How should Finch report findings?", Options: []string{"Summary", "Detailed"}}
					if err := store.finishTurn(ctx, first.ID, Succeeded, "Replied.", "", &TurnAction{Card: card, BudClarification: true, Handoffs: jobs}); err != nil {
						t.Fatal(err)
					}
					changeSettings()
					answer, _, err := store.AnswerCard(ctx, chat, first.ID, 0)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.claimLane(ctx, false); err != nil {
						t.Fatal(err)
					}
					if err := store.finishTurn(ctx, answer.ID, Succeeded, "Replied.", "", &TurnAction{BudProposal: &draft, Handoffs: jobs}); err != nil {
						t.Fatal(err)
					}
					task = answer.ID
				}
				_, err = store.CreateProposedBud(ctx, chat, task, CreateBudWithWork)
				if change == "new folder" {
					if err == nil || !strings.Contains(err.Error(), "working folder") {
						t.Fatalf("approval accepted work in the new folder: %v", err)
					}
					if work, err := store.WorkOverview(ctx, chat); err != nil || len(work) != 0 {
						t.Fatalf("rejected approval left work behind: %+v, %v", work, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var workspace string
				var edits bool
				err = store.db.QueryRowContext(ctx, `SELECT t.repo,ct.allow_edits FROM turn_details td
					JOIN tasks t ON t.id=td.task_id JOIN chat_turns ct ON ct.task_id=t.id
					WHERE td.parent_id=? AND td.title='Fetch logs'`, task).Scan(&workspace, &edits)
				if err != nil || workspace != original || edits {
					t.Fatalf("job lost the original read-only scope: %q, %v, %v", workspace, edits, err)
				}
			})
		}
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
