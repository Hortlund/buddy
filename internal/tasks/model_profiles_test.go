package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyPreferenceSavePreservesWorkerMode(t *testing.T) {
	for _, mode := range []string{"fixed", "auto"} {
		t.Run(mode, func(t *testing.T) {
			ctx, store := t.Context(), teamTestStore(t)
			agent, err := store.Agent(ctx, DefaultAgentID)
			if err != nil {
				t.Fatal(err)
			}
			agent.ModelPreferences = &ModelPreferences{Mode: mode, Effort: "high", Speed: "default"}
			agent, err = store.SaveAgent(ctx, agent.ID, agent)
			if err != nil {
				t.Fatal(err)
			}
			// Protocol-86 clients round-trip the other preference fields but
			// have no mode field in their decoded Agent type.
			data, err := json.Marshal(agent)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			payload["model_preferences"] = json.RawMessage(`{"effort":"high","speed":"default"}`)
			data, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var legacy Agent
			if err := json.Unmarshal(data, &legacy); err != nil {
				t.Fatal(err)
			}
			legacy.Name = "Renamed Bud"
			saved, err := store.SaveAgent(ctx, agent.ID, legacy)
			if err != nil || saved.ModelPreferences == nil || saved.ModelPreferences.Mode != mode {
				t.Fatalf("legacy save changed worker mode: %+v, %v", saved.ModelPreferences, err)
			}
			if legacy.ModelPreferences.Mode != "" {
				t.Fatal("saving mutated the caller's preferences")
			}
			explicit := "auto"
			if mode == "auto" {
				explicit = "fixed"
			}
			legacy.ModelPreferences.Mode = explicit
			saved, err = store.SaveAgent(ctx, agent.ID, legacy)
			if err != nil || saved.ModelPreferences.Mode != explicit {
				t.Fatal("explicit mode change was ignored", err)
			}
		})
	}
}

func TestTaskProfilesKeepQueuedSettingsAndModelDecision(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent.ModelPreferences = &ModelPreferences{Effort: "low", Profiles: []TaskModelProfile{{Kind: "coding", Model: "strong-model", Effort: "high", Speed: "priority"}}}
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "profile-call", "profile", []Handoff{{Title: "Debug API", Brief: "Please debug and test the API."}})
	if err != nil {
		t.Fatal(err)
	}
	queued := jobs[0].Task
	agent.ModelPreferences.Profiles[0].Model = "changed-model"
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	// A client without the new fields must preserve the saved preferences.
	if _, err := store.SaveAgent(ctx, agent.ID, Agent{Name: "Buddy"}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Agent(ctx, agent.ID)
	if err != nil || saved.ModelPreferences.Profiles[0].Model != "changed-model" {
		t.Fatalf("legacy save lost settings: %+v, %v", saved, err)
	}
	turn, err := store.turn(ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &CodexRuntime{MCPStore: store}
	calls := 0
	classify := func(_ context.Context, route Turn, prompt string, _ Emit) (string, error) {
		calls++
		if !strings.Contains(route.isolatedSchema, `"strong-model"`) || !strings.Contains(route.isolatedSchema, `"high"`) || !strings.Contains(prompt, "debug and test") {
			t.Fatal("router did not use configured choices and the request")
		}
		return `{"model":"strong-model","effort":"high","speed":"priority","reason":"Build and verify the API."}`, nil
	}
	turn, err = runtime.selectWorkerModelWith(ctx, turn, profileTestModels(), nil, classify)
	if err != nil || calls != 1 || turn.Agent.Model != "strong-model" || turn.Agent.ModelPreferences.Effort != "high" || turn.Agent.ModelPreferences.Speed != "priority" {
		t.Fatalf("wrong selection: %+v, %v", turn.Agent, err)
	}
	resumed, err := store.turn(ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err = runtime.selectWorkerModelWith(ctx, resumed, profileTestModels(), nil, classify)
	if err != nil || calls != 1 || resumed.Agent.Model != "strong-model" {
		t.Fatalf("resume rerouted saved work: %+v, %v", resumed.Agent, err)
	}
}

func TestWatchTaskProfilesKeepModelDecisionAcrossResume(t *testing.T) {
	ctx, path := t.Context(), filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent.Model = "base-model"
	agent.ModelPreferences = &ModelPreferences{Effort: "low", Profiles: []TaskModelProfile{
		{Kind: "coding", Model: "strong-model", Effort: "high", Speed: "priority"},
	}}
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	watch, err := store.CreateWatch(ctx, Watch{
		ConversationID: DefaultConversationID, Name: "CI", Source: GitHubCI, Scope: "acme/repo",
		Mode: WatchInvestigate, Instructions: "Investigate failed tests.", Budget: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.RecordStandingSignal(ctx, StandingSignal{
		Source: GitHubCI, Scope: watch.Scope, Key: CIWatchKey(123, 1), Title: "Tests failed",
		URL: "https://github.com/acme/repo/actions/runs/123",
	})
	if err != nil || !accepted {
		t.Fatalf("record watch signal: accepted=%v, %v", accepted, err)
	}
	if queued, err := store.enqueueStandingWatch(ctx); err != nil || !queued {
		t.Fatalf("queue watch: queued=%v, %v", queued, err)
	}
	job, err := store.claimLane(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &CodexRuntime{MCPStore: store}
	calls := 0
	classify := func(context.Context, Turn, string, Emit) (string, error) {
		calls++
		if calls > 1 {
			return `{"model":"base-model","effort":"low","speed":"default","reason":"Would change the resumed job."}`, nil
		}
		return `{"model":"strong-model","effort":"high","speed":"priority","reason":"Build and verify the API."}`, nil
	}
	checkProfile := func(stage string, task Task) {
		t.Helper()
		turn, err := store.turn(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		if turn.Origin != WatchOrigin || turn.WatchSignal == nil || turn.Agent.AllowEdits || turn.Agent.Workspace != "" {
			t.Fatalf("%s lost read-only watch context: %+v", stage, turn)
		}
		turn, err = runtime.selectWorkerModelWith(ctx, turn, profileTestModels(), nil, classify)
		p := turn.Agent.ModelPreferences
		if err != nil || calls != 1 || turn.Agent.Model != "strong-model" || p == nil || p.Selected != "automatic" || p.Effort != "high" || p.Speed != "priority" {
			t.Fatalf("%s changed the selected watch profile: calls=%d agent=%+v preferences=%+v, %v", stage, calls, turn.Agent, p, err)
		}
	}
	checkProfile("initial run", job)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	runtime.MCPStore = store
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	retry, err := store.claimLane(ctx, true)
	if err != nil || retry.ID != job.ID {
		t.Fatalf("restart did not retry the same watch: %+v, %v", retry, err)
	}
	checkProfile("server restart", retry)
	if err := store.finishTurn(ctx, retry.ID, NeedsInput, "Waiting for your reply.", "Which test should fail?", nil); err != nil {
		t.Fatal(err)
	}
	inbox, err := store.StandingInbox(ctx)
	if err != nil || len(inbox) != 1 || inbox[0].InputID == "" {
		t.Fatalf("watch clarification: %+v, %v", inbox, err)
	}
	if _, err := store.AnswerWorkInput(ctx, DefaultConversationID, inbox[0].InputID, "TestFixture", "fixture-reply", nil); err != nil {
		t.Fatal(err)
	}
	retry, err = store.claimLane(ctx, true)
	if err != nil || retry.ID != job.ID {
		t.Fatalf("clarification did not resume the same watch: %+v, %v", retry, err)
	}
	checkProfile("clarification reply", retry)
}

func TestTaskProfilesFallbackAndValidation(t *testing.T) {
	for _, response := range []string{`{"profile":"invented"}`, `bad JSON`, ``} {
		turn := Turn{Agent: Agent{Model: "base", ModelPreferences: &ModelPreferences{Effort: "medium", Profiles: []TaskModelProfile{{Kind: "review", Model: "strong", Effort: "high"}}}}}
		got, err := (&CodexRuntime{}).selectWorkerModelWith(t.Context(), turn, profileTestModels(), nil, func(context.Context, Turn, string, Emit) (string, error) {
			if response == "" {
				return response, errors.New("router unavailable")
			}
			return response, nil
		})
		if err != nil || got.Agent.Model != "base" || got.Agent.ModelPreferences.Effort != "medium" {
			t.Fatalf("fallback changed default: %+v, %v", got.Agent, err)
		}
	}
	p := &ModelPreferences{Profiles: []TaskModelProfile{{Kind: "review", Model: "strong"}, {Kind: "review", Model: "other"}}}
	if p.validate() == nil {
		t.Fatal("duplicate task profiles accepted")
	}
	if (&ModelPreferences{Speed: "priority\ninject=true"}).validate() == nil {
		t.Fatal("invalid config option accepted")
	}
}

func TestAppServerAndExecReceiveEffortAndSpeed(t *testing.T) {
	preferences := &ModelPreferences{Effort: "high", Speed: "priority"}
	want := []string{"-c", `model_reasoning_effort="high"`, "-c", `service_tier="priority"`}
	if flags := modelConfigFlags(preferences); !reflect.DeepEqual(flags, want) {
		t.Fatalf("exec flags: %q", flags)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	frames := make(chan appFrame, 1)
	frames <- appFrame{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turn":{"id":"turn","status":"inProgress"}}`)}
	session := appSession{input: write, incoming: frames, threadID: "thread", modelPreferences: preferences, events: codexEvents{emit: func(string, string) error { return nil }}}
	if err := session.startTurn(t.Context(), "test", nil); err != nil {
		t.Fatal(err)
	}
	var frame struct{ Params map[string]any }
	if err := json.NewDecoder(read).Decode(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Params["effort"] != "high" || frame.Params["serviceTierForTurn"] != "priority" {
		t.Fatalf("app-server settings: %v", frame.Params)
	}
}

func TestDeviceProfilesRequireCapableWorkerAndTravelWithJob(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	agent, err := store.Agent(ctx, DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent.ModelPreferences = &ModelPreferences{Effort: "high"}
	if _, err := store.SaveAgent(ctx, agent.ID, agent); err != nil {
		t.Fatal(err)
	}
	device, err := store.EnrollDevice(ctx, "Build machine")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueVoiceAssignments(ctx, DefaultConversationID, DefaultAgentID, "call", "request", []Handoff{{DeviceID: device.ID, Title: "Review", Brief: "Inspect changes.", ReadOnly: true}}); err != nil {
		t.Fatal(err)
	}
	if job, err := store.ReserveExecutionWork(ctx, device.ID, false, false); err != nil || job != nil {
		t.Fatalf("old worker received unsupported preferences: %+v, %v", job, err)
	}
	job, err := store.ReserveModelProfileWork(ctx, device.ID, false, false)
	if err != nil || job == nil || job.Agent.ModelPreferences == nil || job.Agent.ModelPreferences.Effort != "high" {
		t.Fatalf("new worker lost preferences: %+v, %v", job, err)
	}
}

func TestGroupRoutingReplacesModelPreferencesWithChosenBud(t *testing.T) {
	ctx, store := t.Context(), teamTestStore(t)
	chosen, err := store.CreateAgent(ctx, Agent{Name: "Reviewer", Model: "review-model", ModelPreferences: &ModelPreferences{Effort: "high"}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, GroupRequest{Name: "Project", Members: []string{DefaultAgentID, chosen.ID}})
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.Send(ctx, group.ID, "Please review the API.", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err = store.assignGroupTurn(ctx, queued, group.ID, chosen.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, queued)
	if err != nil || turn.Agent.ID != chosen.ID || turn.Agent.Model != "review-model" || turn.Agent.ModelPreferences == nil || turn.Agent.ModelPreferences.Effort != "high" {
		t.Fatalf("group retained the lead's settings: %+v, %v", turn.Agent, err)
	}
}

func TestLookupSchemasDefineEveryRequiredProperty(t *testing.T) {
	for _, schema := range []string{githubReadSchema, connectionReadSchema} {
		var object struct {
			Required   []string
			Properties map[string]json.RawMessage
		}
		if err := json.Unmarshal([]byte(schema), &object); err != nil {
			t.Fatal(err)
		}
		if len(object.Required) != len(object.Properties) {
			t.Fatal("strict schema does not list every property")
		}
		for _, name := range object.Required {
			if _, ok := object.Properties[name]; !ok {
				t.Fatalf("undefined required property %s", name)
			}
		}
	}
}

func profileTestModels() []Model {
	return []Model{
		{ID: DefaultCodexModel, Efforts: []string{"low", "medium"}},
		{ID: "base-model", Efforts: []string{"low", "medium"}},
		{ID: "base", Efforts: []string{"medium"}},
		{ID: "strong-model", Efforts: []string{"high"}, Speeds: []ModelSpeed{{ID: "priority", Name: "Fast"}}},
	}
}
