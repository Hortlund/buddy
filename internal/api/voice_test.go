package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type testVoicePeer struct {
	conn     *websocket.Conn
	received chan object
}

func newTestVoiceProvider(t *testing.T) (realtimeDial, <-chan testVoicePeer) {
	t.Helper()
	peers := make(chan testVoicePeer, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		peer := testVoicePeer{conn, make(chan object, 32)}
		peers <- peer
		ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
		defer cancel()
		for {
			var event object
			if err := wsjson.Read(ctx, conn, &event); err != nil {
				return
			}
			select {
			case peer.received <- event:
			case <-ctx.Done():
				return
			}
			if event["type"] == "session.update" {
				if err := wsjson.Write(ctx, conn, object{"type": "session.updated"}); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	return func(ctx context.Context, key string) (*websocket.Conn, error) {
		if key != "test-voice-secret" {
			return nil, errors.New("wrong voice credential")
		}
		conn, _, err := websocket.Dial(ctx, server.URL, nil)
		return conn, err
	}, peers
}

func TestVoiceRoomRoutesAudioAndHandsOffWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	t.Setenv("BUDDY_VOICE_API_KEY", "")
	dir := t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveLanguages(ctx, tasks.LanguagePreferences{Chat: "en", Voice: "sv"}); err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, tasks.Agent{Name: "Forge", Role: "Developer"})
	if err != nil {
		t.Fatal(err)
	}
	buddy, err := store.Agent(ctx, tasks.DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordVoiceLine(ctx, tasks.DefaultConversationID, "previous-call", buddy, "user", "Earlier private call."); err != nil {
		t.Fatal(err)
	}
	started := make(chan tasks.Turn, 1)
	worker := tasks.NewWorker(store, nil, func(ctx context.Context, turn tasks.Turn, _ tasks.Emit) (string, error) {
		if turn.Title == "Review logs" {
			return "Buddy reviewed the logs.", nil
		}
		if turn.TeamStep == "wrap_up" {
			return "Team wrap-up in the group chat.", nil
		}
		if turn.TeamStep == "contribute" {
			return turn.Agent.Name + " posted a team update.", nil
		}
		started <- turn
		<-ctx.Done()
		return "", ctx.Err()
	})
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(workerCtx) }()
	defer func() { stopWorker(); <-done }()

	dial, peers := newTestVoiceProvider(t)
	mux := http.NewServeMux()
	var voiceInUse atomic.Bool
	workspaceRoutes(mux, store, &voiceInUse)
	voiceRoutes(mux, store, worker, localconfig.VoiceKey{Dir: dir}, dial, &voiceInUse)
	server := startTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()
	const chatID = tasks.DefaultConversationID
	var httpErr *HTTPError
	if _, err := client.OpenVoice(ctx, chatID, ""); !errors.As(err, &httpErr) || httpErr.Status != http.StatusPreconditionFailed {
		t.Fatalf("missing key = %v", err)
	}
	cred, err := client.VoiceKey(ctx, http.MethodPut, "test-voice-secret")
	if err != nil || cred.Source != "saved_key" {
		t.Fatalf("save voice key = %+v, %v", cred, err)
	}
	if key, source, err := (localconfig.CodexKey{Dir: dir}).Read(); err != nil || key == "test-voice-secret" || source == "saved_key" {
		t.Fatalf("voice key affected Codex: source=%s, err=%v", source, err)
	}
	if _, err := client.OpenVoice(ctx, chatID, agent.ID); !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("starting a direct call with an uninvited agent = %v", err)
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, 2*time.Second)
	call, err := client.OpenVoice(dialCtx, chatID, "")
	cancelDial()
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	if err := client.RemoveAgent(ctx, buddy.ID); !errors.As(err, &httpErr) || httpErr.Status != http.StatusConflict {
		t.Fatalf("removing a Bud during a call = %v", err)
	}

	read := func(want string) VoiceEvent {
		t.Helper()
		for {
			event, pcm, err := call.Read(ctx)
			// Work status notifications are asynchronous to the speaking turn.
			if err == nil && len(pcm) == 0 && event.Type == "work_changed" && want != "work_changed" {
				continue
			}
			if err != nil || len(pcm) != 0 || event.Type != want {
				t.Fatalf("call received %+v, %d audio bytes, %v, want %s", event, len(pcm), err, want)
			}
			return event
		}
	}
	send := func(event VoiceEvent) {
		t.Helper()
		if err := call.Send(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	var first, second testVoicePeer
	peer := func() testVoicePeer {
		t.Helper()
		select {
		case p := <-peers:
			return p
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return testVoicePeer{}
		}
	}
	receive := func(p testVoicePeer, want string) object {
		t.Helper()
		select {
		case e := <-p.received:
			if e["type"] != want {
				t.Fatalf("provider received %+v, want %s", e, want)
			}
			return e
		case <-ctx.Done():
			t.Fatalf("provider waiting for %s: %v", want, ctx.Err())
			return nil
		}
	}
	provide := func(p testVoicePeer, typ string, e object) {
		t.Helper()
		e["type"] = typ
		if err := wsjson.Write(ctx, p.conn, e); err != nil {
			t.Fatal(err)
		}
	}
	speak := func(p testVoicePeer, id, text string) {
		t.Helper()
		provide(p, "input_audio_buffer.speech_started", object{"item_id": id})
		read("listening")
		provide(p, "input_audio_buffer.speech_stopped", object{"item_id": id})
		provide(p, "conversation.item.input_audio_transcription.completed", object{"item_id": id, "transcript": text})
		if line := read("final"); line.Text != text || line.Speaker != "You" {
			t.Fatalf("user caption: %+v", line)
		}
		receive(p, "response.create")
		read("responding")
	}
	configuredVoice := func(p testVoicePeer) string {
		e := receive(p, "session.update")
		session := e["session"].(map[string]any)
		audio := session["audio"].(map[string]any)
		input := audio["input"].(map[string]any)
		if input["transcription"].(map[string]any)["language"] != "sv" ||
			!strings.Contains(session["instructions"].(string), "Prefer speaking Swedish") {
			t.Fatalf("call language was not applied: %+v", input)
		}
		return audio["output"].(map[string]any)["voice"].(string)
	}

	first = peer()
	if configuredVoice(first) != "marin" {
		t.Fatal("unexpected first voice")
	}
	receive(first, "conversation.item.create") // Only the original participant gets private chat context.
	if event := read("ready"); len(event.Participants) != 1 || event.Agent != tasks.DefaultAgentID || event.Lead != buddy.ID || event.Call == "" {
		t.Fatalf("solo call: %+v", event)
	}

	pcm := make([]byte, 480)
	send(VoiceEvent{Type: "audio_ready"})
	if req := receive(first, "response.create"); !strings.Contains(req["response"].(map[string]any)["instructions"].(string), "Greet") {
		t.Fatalf("no opening greeting: %+v", req)
	}
	read("responding")
	provide(first, "response.created", object{"response": object{"id": "greeting"}})
	provide(first, "response.output_audio.delta", object{"response_id": "greeting", "item_id": "greeting-audio", "delta": base64.StdEncoding.EncodeToString(pcm)})
	read("audio_start")
	if _, data, err := call.Read(ctx); err != nil || len(data) != len(pcm) {
		t.Fatalf("opening greeting audio len=%d: %v", len(data), err)
	}
	// Direct microphones cannot use voice activity to interrupt speaker playback.
	send(VoiceEvent{Type: "interrupt"})
	if event := read("interrupt"); event.Item != "greeting-audio" {
		t.Fatalf("manual interrupt: %+v", event)
	}
	receive(first, "response.cancel")
	send(VoiceEvent{Type: "cut", Item: "greeting-audio", Millis: 3})
	receive(first, "conversation.item.truncate")
	provide(first, "response.done", object{"response": object{"id": "greeting", "status": "cancelled"}})
	send(VoiceEvent{Type: "audio_ready"}) // Duplicate device notifications must not re-greet.

	if err := call.Audio(ctx, pcm); err != nil {
		t.Fatal(err)
	}
	receive(first, "input_audio_buffer.append")
	speak(first, "user1", "Hello Buddy. Invite Forge.")
	provide(first, "response.created", object{"response": object{"id": "r1"}})
	provide(first, "response.output_audio.delta", object{"response_id": "r1", "item_id": "a1", "delta": base64.StdEncoding.EncodeToString(pcm)})
	read("audio_start")
	if _, got, err := call.Read(ctx); err != nil || len(got) != len(pcm) {
		t.Fatalf("audio len=%d err=%v", len(got), err)
	}
	provide(first, "response.output_audio_transcript.done", object{"response_id": "r1", "item_id": "a1", "transcript": "Hello from Buddy."})
	read("caption")
	provide(first, "response.done", object{"response": object{"id": "r1", "status": "completed", "output": []object{{
		"type": "function_call", "name": "invite_agent", "call_id": "invite1", "arguments": `{"agent_id":"` + agent.ID + `"}`,
	}}}})
	read("audio_end")
	read("joining")
	second = peer()
	if configuredVoice(second) != "cedar" {
		t.Fatal("unexpected second voice")
	}
	history := receive(second, "conversation.item.create")["item"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(history, "Hello Buddy. Invite Forge.") || strings.Contains(history, "Earlier private call.") {
		t.Fatalf("invite received the wrong context: %s", history)
	}
	group := read("roster")
	if len(group.Participants) != 2 || group.Chat == "" || group.Chat == chatID || group.Title == "" || group.Lead != buddy.ID {
		t.Fatalf("group call: %+v", group)
	}
	inviteOutput := receive(first, "conversation.item.create")["item"].(map[string]any)["output"].(string)
	if !strings.Contains(inviteOutput, "joined the call and group chat") {
		t.Fatalf("invite output: %q", inviteOutput)
	}

	send(VoiceEvent{Type: "focus", Agent: agent.ID})
	if event := read("roster"); event.Agent != tasks.DefaultAgentID || event.Next != agent.ID {
		t.Fatalf("focus changed during playback: %+v", event)
	}
	send(VoiceEvent{Type: "played", Item: "a1"})
	receive(second, "conversation.item.create")
	read("final")
	if event := read("roster"); event.Agent != agent.ID {
		t.Fatalf("the new agent did not get the next turn: %+v", event)
	}
	greeting := receive(second, "response.create")["response"].(map[string]any)
	if greeting["tool_choice"] != "none" || !strings.Contains(greeting["instructions"].(string), "natural hello") {
		t.Fatalf("the invitee did not receive a greeting turn: %+v", greeting)
	}
	if !strings.Contains(greeting["instructions"].(string), "Lead Bud in this call is Buddy") {
		t.Fatalf("invitee doesn't know the lead: %+v", greeting)
	}
	read("responding")
	provide(second, "response.created", object{"response": object{"id": "join-greeting"}})
	provide(second, "response.output_audio_transcript.done", object{"response_id": "join-greeting", "transcript": "Hi, everyone."})
	read("caption")
	provide(second, "response.done", object{"response": object{"id": "join-greeting", "status": "completed"}})
	read("final")
	read("settled")
	receive(first, "conversation.item.create")

	send(VoiceEvent{Type: "mute", Muted: true})
	receive(first, "input_audio_buffer.clear")
	if err := call.Audio(ctx, pcm); err != nil {
		t.Fatal(err)
	}
	send(VoiceEvent{Type: "mute", Muted: false})
	if err := call.Audio(ctx, pcm); err != nil {
		t.Fatal(err)
	}
	receive(first, "input_audio_buffer.append")
	provide(first, "input_audio_buffer.speech_started", object{"item_id": "user2"})
	read("listening")
	provide(first, "input_audio_buffer.speech_stopped", object{"item_id": "user2"})
	provide(first, "conversation.item.input_audio_transcription.completed", object{
		"item_id": "user2", "transcript": "Hand over to Forge. Forge, prepare a status report, and Buddy, review the logs.",
	})
	read("final")
	receive(second, "conversation.item.create")
	if request := receive(second, "response.create")["response"].(map[string]any); request["tool_choice"] == "none" ||
		!strings.Contains(request["instructions"].(string), "question or task they included") {
		t.Fatalf("compound handoff lost the caller's task: %+v", request)
	}
	read("responding")
	provide(second, "response.created", object{"response": object{"id": "r2"}})
	provide(second, "response.done", object{"response": object{"id": "r2", "status": "completed", "output": []object{{
		"type": "function_call", "name": "delegate_work", "call_id": "tool1", "arguments": `{"assignments":[
			{"title":"Status report","brief":"Prepare the status report."},
			{"agent_id":"` + buddy.ID + `","title":"Review logs","brief":"Read the logs.","read_only":true}]}`,
	}}}})
	read("settled")
	job := read("work_queued")
	if job.Item == "" || job.Text != "Status report" {
		t.Fatalf("new job was not surfaced in the call: %+v", job)
	}
	if output := receive(second, "conversation.item.create")["item"].(map[string]any)["output"].(string); !strings.HasPrefix(output, "The handoff was accepted.") ||
		!strings.Contains(output, "Review logs") || !strings.Contains(output, "Forge") {
		t.Fatalf("tool output: %v", output)
	}
	if request := receive(second, "response.create"); request["response"].(map[string]any)["tool_choice"] != "none" {
		t.Fatalf("the tool confirmation could start another job: %+v", request)
	}
	read("responding")
	select {
	case turn := <-started:
		if turn.Agent.ID != agent.ID || turn.Agent.Workspace != agent.Workspace || turn.Title != "Status report" {
			t.Fatalf("wrong handoff target: %+v", turn)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitFor(t, ctx, func() bool {
		items, err := store.WorkOverview(ctx, group.Chat)
		if err != nil || len(items) != 2 {
			return false
		}
		for _, item := range items {
			if item.Title == "Review logs" && item.AgentID == buddy.ID && item.Status == tasks.Succeeded {
				return true
			}
		}
		return false
	})

	provide(second, "response.created", object{"response": object{"id": "r3"}})
	provide(second, "response.output_audio.delta", object{"response_id": "r3", "item_id": "a3", "delta": base64.StdEncoding.EncodeToString(pcm)})
	read("audio_start")
	if _, _, err := call.Read(ctx); err != nil {
		t.Fatal(err)
	}
	// While Forge speaks, the mic still feeds the original provider, and an
	// interruption from that provider cancels Forge's output.
	if err := call.Audio(ctx, pcm); err != nil {
		t.Fatal(err)
	}
	receive(first, "input_audio_buffer.append")
	provide(first, "input_audio_buffer.speech_started", object{"item_id": "user3"})
	if e := read("interrupt"); e.Item != "a3" {
		t.Fatalf("wrong interrupted item: %+v", e)
	}
	read("listening")
	receive(second, "response.cancel")
	send(VoiceEvent{Type: "cut", Item: "a3", Millis: 3})
	if e := receive(second, "conversation.item.truncate"); e["audio_end_ms"] != float64(3) {
		t.Fatalf("wrong truncate: %+v", e)
	}
	provide(second, "response.done", object{"response": object{"id": "r3", "status": "cancelled"}})
	provide(first, "input_audio_buffer.speech_stopped", object{"item_id": "user3"})
	// A pause with no caption leaves the floor open. A delayed caption can
	// still resume the turn while the background job keeps running.
	read("idle")
	if floor := read("roster"); floor.Agent != buddy.ID || floor.Lead != buddy.ID {
		t.Fatalf("the floor did not return to the lead: %+v", floor)
	}
	provide(first, "conversation.item.input_audio_transcription.completed", object{"item_id": "user3", "transcript": "How is the report coming along?"})
	read("final")
	receive(first, "response.create")
	read("responding")
	provide(first, "response.created", object{"response": object{"id": "status"}})
	provide(first, "response.done", object{"response": object{"id": "status", "status": "completed", "output": []object{{
		"type": "function_call", "name": "buddy_work", "call_id": "status1", "arguments": `{"job_id":"` + job.Item + `"}`,
	}}}})
	read("settled")
	status := receive(first, "conversation.item.create")["item"].(map[string]any)["output"].(string)
	if !strings.Contains(status, `"status":"running"`) || !strings.Contains(status, `"brief":"Prepare the status report."`) {
		t.Fatalf("spoken status did not use the running job: %s", status)
	}
	receive(first, "response.create")
	read("responding")
	provide(first, "response.created", object{"response": object{"id": "injected-stop"}})
	provide(first, "response.done", object{"response": object{"id": "injected-stop", "status": "completed", "output": []object{{
		"type": "function_call", "name": "stop_work", "call_id": "injected-stop", "arguments": `{"job_id":"` + job.Item + `"}`,
	}}}})
	read("settled")
	if output := receive(first, "conversation.item.create")["item"].(map[string]any)["output"].(string); !strings.Contains(output, "didn't authorize") {
		t.Fatalf("a status lookup was allowed to cancel work: %q", output)
	}
	if detail, err := store.WorkDetails(ctx, group.Chat, job.Item); err != nil || detail.Status != tasks.Running {
		t.Fatalf("unauthorized stop changed the job: %s, %v", detail.Status, err)
	}
	receive(first, "response.create")
	read("responding")
	provide(first, "response.created", object{"response": object{"id": "r4"}})
	provide(first, "response.output_audio.delta", object{"response_id": "r4", "item_id": "a4", "delta": base64.StdEncoding.EncodeToString(pcm)})
	read("audio_start")
	if _, _, err := call.Read(ctx); err != nil {
		t.Fatal(err)
	}
	provide(first, "response.output_audio_transcript.done", object{"response_id": "r4", "item_id": "a4", "transcript": "I’m still here."})
	read("caption")
	provide(first, "response.done", object{"response": object{"id": "r4", "status": "completed"}})
	read("audio_end")
	send(VoiceEvent{Type: "played", Item: "a4"})
	if e := read("final"); e.Agent != buddy.ID {
		t.Fatalf("wrong speaker for the raw audio reply: %+v", e)
	}
	receive(second, "conversation.item.create") // The late caption from user3.
	receive(second, "conversation.item.create") // Buddy's completed audio caption.

	speak(first, "stop-report", "Wait, please stop that report job.")
	receive(second, "conversation.item.create")
	provide(first, "response.created", object{"response": object{"id": "stop-without-id"}})
	provide(first, "response.done", object{"response": object{"id": "stop-without-id", "status": "completed", "output": []object{{
		"type": "function_call", "name": "stop_work", "call_id": "missing-id", "arguments": `{"job_id":""}`,
	}}}})
	read("settled")
	if output := receive(first, "conversation.item.create")["item"].(map[string]any)["output"].(string); !strings.Contains(output, "buddy_work") {
		t.Fatalf("missing job ID did not ask for a lookup: %q", output)
	}
	if request := receive(first, "response.create")["response"].(map[string]any); len(request["tools"].([]any)) != 2 {
		t.Fatalf("missing job ID did not retain just the stop and lookup tools: %+v", request)
	}
	read("responding")
	provide(first, "response.created", object{"response": object{"id": "stop-job-lookup"}})
	provide(first, "response.done", object{"response": object{"id": "stop-job-lookup", "status": "completed", "output": []object{{
		"type": "function_call", "name": "buddy_work", "call_id": "lookup-id", "arguments": `{}`,
	}}}})
	read("settled")
	if output := receive(first, "conversation.item.create")["item"].(map[string]any)["output"].(string); !strings.Contains(output, job.Item) || strings.Contains(output, "latest_update") {
		t.Fatalf("stop lookup did not return only job metadata: %q", output)
	}
	receive(first, "response.create")
	read("responding")
	provide(first, "response.created", object{"response": object{"id": "stop-report-reply"}})
	provide(first, "response.done", object{"response": object{"id": "stop-report-reply", "status": "completed", "output": []object{{
		"type": "function_call", "name": "stop_work", "call_id": "stop1", "arguments": `{"job_id":"` + job.Item + `"}`,
	}}}})
	read("settled")
	if output := receive(first, "conversation.item.create")["item"].(map[string]any)["output"].(string); !strings.Contains(output, "job is stopping") {
		t.Fatalf("spoken stop did not acknowledge cancellation: %q", output)
	}
	if request := receive(first, "response.create"); request["response"].(map[string]any)["tool_choice"] != "none" {
		t.Fatalf("stop confirmation allowed more tools: %+v", request)
	}
	read("responding")
	provide(first, "response.created", object{"response": object{"id": "stop-report-confirm"}})
	provide(first, "response.done", object{"response": object{"id": "stop-report-confirm", "status": "completed"}})
	read("settled")
	waitFor(t, ctx, func() bool {
		detail, err := store.WorkDetails(ctx, group.Chat, job.Item)
		return err == nil && detail.Status == tasks.Cancelled
	})

	// The caller addresses a named Bud while the lead receives the mic.
	provide(first, "input_audio_buffer.speech_started", object{"item_id": "user4"})
	read("listening")
	provide(first, "input_audio_buffer.speech_stopped", object{"item_id": "user4"})
	provide(first, "conversation.item.input_audio_transcription.completed", object{
		"item_id": "user4", "transcript": "Forge, pass the floor to Buddy.",
	})
	read("final")
	directed := receive(second, "conversation.item.create")["item"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(directed, "Forge, pass the floor to Buddy.") {
		t.Fatalf("the named agent did not get the caller's turn: %q", directed)
	}
	if floor := read("roster"); floor.Agent != agent.ID || floor.Lead != buddy.ID {
		t.Fatalf("saying Forge's name did not route the reply: %+v", floor)
	}
	receive(second, "response.create")
	read("responding")
	provide(second, "response.created", object{"response": object{"id": "ask"}})
	provide(second, "response.done", object{"response": object{"id": "ask", "status": "completed", "output": []object{{
		"type": "function_call", "name": "ask_agent", "call_id": "ask1", "arguments": `{"agent_id":"` + buddy.ID + `"}`,
	}}}})
	read("settled")
	output := receive(second, "conversation.item.create")
	if floor := read("roster"); floor.Agent != buddy.ID {
		t.Fatalf("Forge's handoff did not give Buddy the floor: %+v", floor)
	}
	request := receive(first, "response.create")
	handoff := request["response"].(map[string]any)
	if !strings.Contains(handoff["instructions"].(string), "acknowledge the handoff") || handoff["tool_choice"] != "none" {
		t.Fatalf("Buddy did not get a simple handoff: %+v", handoff)
	}
	read("responding")
	// A rejected handoff request or tool output must not end the call.
	provide(second, "error", object{"error": object{"code": "invalid_value", "event_id": output["event_id"]}})
	provide(first, "error", object{"error": object{"type": "server_error"}})
	read("settled")
	if notice := read("notice"); !strings.Contains(notice.Text, "still live") {
		t.Fatalf("no recovery notice: %+v", notice)
	}
	provide(first, "response.created", object{"response": object{"id": "late", "metadata": handoff["metadata"]}})
	if cancel := receive(first, "response.cancel"); cancel["response_id"] != "late" {
		t.Fatalf("a late reply was not cancelled: %+v", cancel)
	}

	// An open question goes to Buddy after Forge spoke, without a routing model call.
	speak(first, "user5", "Could you keep working on the report together after I hang up?")
	receive(second, "conversation.item.create")
	provide(first, "response.created", object{"response": object{"id": "continue"}})
	provide(first, "response.done", object{"response": object{"id": "continue", "status": "completed", "output": []object{{
		"type": "function_call", "name": "continue_in_chat", "call_id": "team1", "arguments": `{"title":"Finish report","brief":"Finish the report as a team."}`,
	}}}})
	read("settled")
	read("team_queued")
	if result := receive(first, "conversation.item.create")["item"].(map[string]any)["output"].(string); !strings.Contains(result, "when the call ends") {
		t.Fatalf("wrong post-call confirmation: %q", result)
	}
	receive(first, "response.create")
	read("responding")
	send(VoiceEvent{Type: "hangup"})
	call.Close()

	waitFor(t, ctx, func() bool {
		chat, err := store.Conversation(ctx, group.Chat, 0)
		if err != nil {
			return false
		}
		var captions []string
		teamFinished := false
		for _, m := range chat.Messages {
			if m.Origin == "voice" {
				captions = append(captions, m.Text)
			}
			if m.Text == "Team wrap-up in the group chat." && m.AgentID == buddy.ID {
				teamFinished = true
			}
		}
		want := "Hello Buddy. Invite Forge.|Hello from Buddy.|Hi, everyone.|Hand over to Forge. Forge, prepare a status report, and Buddy, review the logs.|How is the report coming along?|I’m still here.|Wait, please stop that report job.|Forge, pass the floor to Buddy.|Could you keep working on the report together after I hang up?"
		return teamFinished && strings.Join(captions, "|") == want
	})
	private, err := store.Conversation(ctx, chatID, 0)
	if err != nil || len(private.Messages) != 1 || private.Messages[0].Text != "Earlier private call." {
		t.Fatalf("private chat history changed: %+v, %v", private.Messages, err)
	}
	if err := client.RemoveAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenVoice(ctx, agent.ConversationID, ""); !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("opening a voice call with a removed Bud = %v", err)
	}
}

func TestSpokenWorkStopRequiresADirectCommand(t *testing.T) {
	agents := []tasks.AgentRef{{ID: "ben", Name: "Ben"}}
	for _, test := range []struct {
		text, recent string
		allowed      bool
		target       string
	}{
		{text: "Wait, please stop that report job.", allowed: true},
		{text: "Hey Ben, please cancel the build.", allowed: true},
		{text: "Ben, kan du avbryta jobbet?", allowed: true},
		{text: "Ben, kan du stoppa jobbet?", allowed: true},
		{text: "Vänta, gör inte det.", recent: "job-1", allowed: true, target: "job-1"},
		{text: "Wait, don't do that.", recent: "job-1", allowed: true, target: "job-1"},
		{text: "Wait, don't do that."},
		{text: "How is the job going?", recent: "job-1"},
		{text: "Don't stop the job.", recent: "job-1"},
		{text: "What happens if I stop the job?"},
		{text: "Can you stop the timer for the job?"},
		{text: "Stop telling me about the job."},
		{text: "Stop.", recent: "job-1"},
		{text: `"Stop the job."`},
	} {
		allowed, target := spokenWorkStop(test.text, agents, test.recent)
		if allowed != test.allowed || target != test.target {
			t.Errorf("spokenWorkStop(%q) = %v, %q, want %v, %q", test.text, allowed, target, test.allowed, test.target)
		}
	}
}

func TestSpokenAddressMatchesAgentNamesWithoutMatchingMentions(t *testing.T) {
	agents := []tasks.AgentRef{
		{ID: "forge", Name: "Forge"}, {ID: "forge-lab", Name: "Forge Lab"},
		{ID: "i", Name: "I"}, {ID: "dee", Name: "D"}, {ID: "one", Name: "Scout"}, {ID: "two", Name: "Scout"},
		{ID: "buddy", Name: "Buddyyyyy"},
	}
	for phrase, want := range map[string]string{
		"Hey, Forge Lab, can you take a look?": "forge-lab",
		"What do you think, Forge?":            "forge",
		"Please invite Forge to this call.":    "",
		"I wonder what Forge thinks.":          "",
		"I, are you here?":                     "i",
		"Scout, can you help?":                 "",
		"Hej, de, hur går det?":                "dee",
		"Buddy, hör du mig?":                   "buddy",
		"Lämna över ordet till Forge Lab.":     "forge-lab",
		"Kan jag prata med D?":                 "dee",
	} {
		if got, _ := voiceAddress(phrase, agents, ""); got != want {
			t.Errorf("address for %q = %q, want %q", phrase, got, want)
		}
	}
}

func TestSpokenHandoffPreservesQuestionAndExactName(t *testing.T) {
	agents := []tasks.AgentRef{{ID: "stretch", Name: "Nikkoooo"}, {ID: "exact", Name: "Nikko"},
		{ID: "letter", Name: "B"}, {ID: "be", Name: "Be"}}
	for _, tt := range []struct {
		text, id  string
		floorOnly bool
	}{
		{"Nikko, can you help?", "exact", false},
		{"Nikkoo, can you help?", "", false},
		{"Nikkooooo, can you help?", "", false}, // Both configured names match approximately.
		{"Be, can you help?", "be", false},
		{"Can I speak to Nikko?", "exact", true},
		{"Hand over to Nikko and ask why the build failed.", "exact", false},
		{"Hand over to B and ask why the build failed.", "letter", false},
		{"Pass the floor to the lead Bud.", "lead", true},
		{"Pass the floor to the lead Bud and ask about the build.", "lead", false},
	} {
		id, mode := voiceAddress(tt.text, agents, "lead")
		if floorOnly := mode == addressFloorOnly; id != tt.id || floorOnly != tt.floorOnly {
			t.Errorf("%q routed to %q, floor only %v, want %q, floor only %v", tt.text, id, floorOnly, tt.id, tt.floorOnly)
		}
	}
}
