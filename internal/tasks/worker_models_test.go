package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func workerModelCatalog() []Model {
	return []Model{
		{ID: "base", Efforts: []string{"low", "medium"}},
		{ID: "coding-model", Efforts: []string{"high", "xhigh"}, Speeds: []ModelSpeed{{ID: "priority", Name: "Fast"}}},
		{ID: "small-model", Efforts: []string{"low"}},
	}
}

func TestWorkerChoosesSettingsForActualAssignment(t *testing.T) {
	for _, test := range []struct{ name, answer, model, effort string }{
		{"substantial coding", `{"model":"coding-model","effort":"high","speed":"default","reason":"Cross-module change with integration tests."}`, "coding-model", "high"},
		{"simple mail", `{"model":"small-model","effort":"low","speed":"default","reason":"Straightforward inbox summary."}`, "small-model", "low"},
		{"unsupported effort for selected model", `{"model":"small-model","effort":"xhigh","speed":"default","reason":"Invalid."}`, "base", ""},
		{"unsupported speed for selected model", `{"model":"base","effort":"low","speed":"priority","reason":"Invalid."}`, "base", ""},
		{"invented model", `{"model":"invented","effort":"high","speed":"default","reason":"Invalid."}`, "base", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			turn := Turn{Agent: Agent{Model: "base"}, Brief: test.name, Title: "Work"}
			got, err := (&CodexRuntime{}).selectWorkerModelWith(t.Context(), turn, workerModelCatalog(), nil, func(_ context.Context, decision Turn, prompt string, _ Emit) (string, error) {
				if decision.isolatedSchema == "" || !strings.Contains(prompt, test.name) || !strings.Contains(prompt, "xhigh") || decision.chatTools != nil {
					t.Fatal("decision did not receive task context and a bounded catalog")
				}
				return test.answer, nil
			})
			if err != nil || got.Agent.Model != test.model {
				t.Fatalf("worker choice: %+v, %v", got.Agent, err)
			}
			if test.effort == "" {
				if got.Agent.ModelPreferences != nil {
					t.Fatal("invalid choice replaced the default settings")
				}
			} else if p := got.Agent.ModelPreferences; p == nil || p.Selected != "automatic" || p.Effort != test.effort {
				t.Fatalf("worker effort: %+v", p)
			}
		})
	}
}

func TestWorkerModelChoiceIsSavedAndFixedSettingsSkipDecision(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	jobs, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "automatic", []Handoff{{Title: "Implement API", Brief: "Implement and test the API."}})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, jobs[0].Task)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &CodexRuntime{MCPStore: store}
	calls := 0
	decide := func(context.Context, Turn, string, Emit) (string, error) {
		calls++
		return `{"model":"coding-model","effort":"high","speed":"default","reason":"Implementation and testing."}`, nil
	}
	if _, err := runtime.selectWorkerModelWith(ctx, turn, workerModelCatalog(), nil, decide); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.turn(ctx, jobs[0].Task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.selectWorkerModelWith(ctx, resumed, workerModelCatalog(), nil, decide); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || resumed.Agent.Model != "coding-model" || resumed.Agent.ModelPreferences.Effort != "high" {
		t.Fatalf("resumed settings: %+v", resumed.Agent)
	}
	turn.Agent.ModelPreferences = &ModelPreferences{Mode: "fixed", Effort: "medium"}
	fixed, err := runtime.selectWorkerModelWith(ctx, turn, workerModelCatalog(), nil, decide)
	if err != nil || calls != 1 || fixed.Agent.ModelPreferences.Effort != "medium" {
		t.Fatal("fixed settings were overridden", err)
	}
	chat := Turn{Agent: Agent{Model: "base", ModelPreferences: &ModelPreferences{Mode: "auto", Effort: "high"}}}
	chat, err = runtime.selectTaskModel(ctx, chat, nil)
	if err != nil || chat.Agent.ModelPreferences.Effort != "" {
		t.Fatal("automatic chat retained a hidden effort override", err)
	}
}

func TestRemoteWorkerSavesDecisionWithActiveLease(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	device, err := store.EnrollDevice(ctx, "Mac")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "remote", []Handoff{{
		DeviceID: device.ID, Title: "Build", Brief: "Build and verify the project.", ExecutionPlan: ExecutionPlan{WorkspaceMode: "checkout"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if job, err := store.ReserveModelProfileWork(ctx, device.ID, false, false); err != nil || job != nil {
		t.Fatal("old worker received workspace planning", job, err)
	}
	job, err := store.ReserveWorkerPlanningWork(ctx, device.ID, false, false)
	if err != nil || job == nil || !job.ModelSelection || job.WorkspaceMode != "checkout" {
		t.Fatalf("remote job: %+v, %v", job, err)
	}
	choice := WorkerModelSelection{Model: "coding-model", Effort: "high", Speed: "default", Reason: "Build and verification."}
	if err := store.RecordDeviceModelSelection(ctx, device.ID, job.ID, "wrong", choice); !errors.Is(err, ErrInactive) {
		t.Fatal("wrong lease changed model", err)
	}
	if err := store.RecordDeviceModelSelection(ctx, device.ID, job.ID, job.Lease, choice); err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, jobs[0].Task)
	if err != nil || turn.Agent.Model != choice.Model || turn.Agent.ModelPreferences == nil || turn.Agent.ModelPreferences.Effort != choice.Effort || turn.Agent.ModelPreferences.Selected != "automatic" {
		t.Fatalf("saved remote selection: %+v, %v", turn.Agent, err)
	}
	if err := store.finishTurn(ctx, job.ID, NeedsInput, "Waiting.", "Which target?", nil); err != nil {
		t.Fatal(err)
	}
	work, err := store.WorkDetails(ctx, DefaultConversationID, job.ID)
	if err != nil || work.InputID == "" {
		t.Fatal("missing worker question", err)
	}
	if _, err := store.AnswerWorkInput(ctx, DefaultConversationID, work.InputID, "macOS", "answer", nil); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.ReserveWorkerPlanningWork(ctx, device.ID, false, false)
	if err != nil || resumed == nil || resumed.ID != job.ID || resumed.Agent.Model != choice.Model || resumed.Agent.ModelPreferences.Effort != choice.Effort {
		t.Fatalf("remote resume: %+v, %v", resumed, err)
	}
	if err := store.RecordDeviceModelSelection(ctx, device.ID, job.ID, job.Lease, choice); !errors.Is(err, ErrInactive) {
		t.Fatal("expired lease changed resumed work", err)
	}
}

func TestAutomaticWorkerChoiceAdaptsToDifferentHostCatalog(t *testing.T) {
	turn := Turn{Agent: Agent{Model: "previous-host-model", ModelPreferences: &ModelPreferences{Mode: "auto", Selected: "automatic", Effort: "ultra"}}, Brief: "Review the code."}
	got, err := (&CodexRuntime{}).selectWorkerModelWith(t.Context(), turn, workerModelCatalog(), nil,
		func(_ context.Context, decision Turn, _ string, _ Emit) (string, error) {
			if decision.Agent.Model != "base" {
				t.Fatal("decision used an unavailable host model")
			}
			return `{"model":"coding-model","effort":"high","speed":"default","reason":"Review on this host."}`, nil
		})
	if err != nil || got.Agent.Model != "coding-model" || got.Agent.ModelPreferences.Effort != "high" {
		t.Fatalf("host adaptation: %+v, %v", got.Agent, err)
	}
}
