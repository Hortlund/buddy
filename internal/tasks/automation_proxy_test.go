package tasks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomationProxyHelper(t *testing.T) {
	switch os.Getenv("BUDDY_PROXY_FIXTURE") {
	case "proxy":
		for index, arg := range os.Args {
			if arg == "--" {
				if err := RunAutomationProxy(context.Background(), os.Args[index+1:]); err != nil {
					os.Exit(2)
				}
				os.Exit(0)
			}
		}
	case "driver":
		var data bytes.Buffer
		_ = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)))
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var request struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(scanner.Bytes(), &request)
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(data.Bytes())}}}})
		}
		os.Exit(0)
	}
}

func TestAutomationProxyCapturesScreensAndHonorsHumanControl(t *testing.T) {
	dir := t.TempDir()
	driver := filepath.Join(dir, "driver")
	// A separate test process behaves as the provider. The parent exercises the
	// real stdio proxy and never touches a desktop or a paid model.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nBUDDY_PROXY_FIXTURE=driver exec '" + exe + "' -test.run=TestAutomationProxyHelper\n"
	if err := os.WriteFile(driver, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "paused"), []byte("paused"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clicked := make(chan struct{}, 1)
	resumeGuard := make(chan struct{})
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var call automationCall
		if err := json.NewDecoder(req.Body).Decode(&call); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch call.Name {
		case "get_desktop_state":
		case "click":
			if capture := call.Capture; capture == nil || capture.Tool != "get_desktop_state" || capture.Width != 2 || capture.Height != 2 || !validExecutionImage(capture.Image) {
				t.Errorf("click lacked its visual evidence: %+v", call.Capture)
			}
			if call.Revision == 0 {
				clicked <- struct{}{}
				select {
				case <-resumeGuard:
				case <-req.Context().Done():
					return
				}
			}
		default:
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer guard.Close()
	cmd := exec.CommandContext(ctx, exe, "-test.run=TestAutomationProxyHelper", "--", "-frames", dir, "-provider", "cua", "-guard", guard.URL, "--", driver)
	cmd.Env = append(os.Environ(), "BUDDY_PROXY_FIXTURE=proxy")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); input.Close(); _ = cmd.Wait() }()
	_, _ = input.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_desktop_state"}}` + "\n"))
	response := make(chan []byte, 1)
	go func() { line, _ := bufio.NewReader(output).ReadBytes('\n'); response <- line }()
	select {
	case <-response:
		t.Fatal("automation acted while a human owned control")
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.Remove(filepath.Join(dir, "paused")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-response:
		var message map[string]any
		if json.Unmarshal(line, &message) != nil || message["id"] != float64(1) {
			t.Fatalf("proxy changed MCP response: %s", line)
		}
	case <-ctx.Done():
		t.Fatal("proxy did not resume")
	}
	data, err := os.ReadFile(filepath.Join(dir, "frame.json"))
	if err != nil {
		t.Fatal(err)
	}
	var frame ExecutionFrame
	if json.Unmarshal(data, &frame) != nil || frame.Provider != "cua" || !validExecutionImage(frame.Data) {
		t.Fatal("proxy did not capture a valid screenshot")
	}
	for _, call := range []string{
		`{"name":"run_script","arguments":{"script":"1+1"}}`,
		`{"name":"launch_app","arguments":{"name":"Calculator","additional_arguments":["--script"]}}`,
		`{"name":"get_window_state","arguments":{"screenshot_out_file":"/tmp/screenshot.png"}}`,
	} {
		_, _ = input.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":` + call + "}\n"))
		go func() { line, _ := bufio.NewReader(output).ReadBytes('\n'); response <- line }()
		select {
		case line := <-response:
			var message struct {
				ID     int
				Result struct{ IsError bool }
			}
			if json.Unmarshal(line, &message) != nil || message.ID != 2 || !message.Result.IsError {
				t.Fatalf("inspection forwarded %s to provider: %s", call, line)
			}
		case <-ctx.Done():
			t.Fatalf("no response for %s", call)
		}
	}
	readResult := func(id int, denied bool) {
		t.Helper()
		go func() { line, _ := bufio.NewReader(output).ReadBytes('\n'); response <- line }()
		select {
		case line := <-response:
			var message struct {
				ID     int
				Result struct{ IsError bool }
			}
			if json.Unmarshal(line, &message) != nil || message.ID != id || message.Result.IsError != denied {
				t.Fatalf("unexpected response for %d: %s", id, line)
			}
		case <-ctx.Done():
			t.Fatalf("no response for %d", id)
		}
	}
	click := func(id string) {
		_, _ = input.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"click","arguments":{"x":1,"y":1}}}` + "\n"))
	}
	click("3")
	select {
	case <-clicked:
	case <-ctx.Done():
		t.Fatal("click didn't reach the guard")
	}
	control := filepath.Join(dir, "control.json")
	if err := writePrivateExecution(control, []byte(`{"paused":true,"revision":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateExecution(control, []byte(`{"paused":false,"revision":2}`)); err != nil {
		t.Fatal(err)
	}
	close(resumeGuard)
	readResult(3, true)
	click("4")
	readResult(4, true)
	_, _ = input.Write([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_desktop_state"}}` + "\n"))
	readResult(5, false)
	click("6")
	readResult(6, false)
}

func TestAutomationGuardAuthorizesIndividualChanges(t *testing.T) {
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
input=$(cat)
case "$input" in
  *'"live_caller_corrections":["Leave ticket A open."]'*) verdict=false ;;
  *'"authenticated_scope":"Inspect https://desk.example and Calculator"'*'"url":"https://desk.example/tickets"'*) verdict=true ;;
  *'"authenticated_scope":"Inspect https://desk.example and Calculator"'*'"name":"Calculator"'*) verdict=true ;;
  *'"authenticated_scope":"Archive ticket A only"'*'"target":"Archive ticket A"'*) verdict=true ;;
  *) verdict=false ;;
esac
printf '{"type":"item.completed","item":{"type":"agent_message","text":"{\\"allowed\\":%s}"}}\n' "$verdict"
`)
	plan := ExecutionPlan{Requirements: []string{"browser", "desktop"}, AutomationWriteScope: "Archive ticket A only", AutomationInspectionScope: "Inspect https://desk.example and Calculator"}
	ctx := t.Context()
	store := teamTestStore(t)
	task := startChatRequest(t, store, "Archive ticket A on Desk")
	runtime.MCPStore = store
	endpoint, closeGuard, err := runtime.startAutomationGuard(ctx, Turn{ExecutionPlan: plan, Agent: Agent{Model: "fixture"}, Brief: "Archive ticket A", BuddyTeam: TeamContext{CurrentTaskID: task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGuard()
	before := currentAutomationEpoch(ctx, endpoint, AutomationControl{})
	if before == "" {
		t.Fatal("automation guard did not expose the current grant epoch")
	}
	for _, check := range []struct {
		provider, tool, args string
		allowed              bool
	}{
		{"playwright", "browser_click", `{"target":"Archive ticket A"}`, true},
		{"playwright", "browser_click", `{"target":"Archive ticket B"}`, false},
		{"playwright", "browser_run_code", `{"target":"Archive ticket A"}`, false},
		{"playwright", "browser_navigate", `{"url":"https://desk.example/tickets"}`, true},
		{"playwright", "browser_navigate", `{"url":"http://127.0.0.1:8000/"}`, false},
		{"cua", "launch_app", `{"name":"Calculator"}`, true},
		{"cua", "launch_app", `{"name":"Messages"}`, false},
	} {
		call := automationCall{Provider: check.provider, Name: check.tool, Arguments: json.RawMessage(check.args)}
		if got := call.actionAllowed(ctx, endpoint); got != check.allowed {
			t.Fatalf("%s %s allowed=%v", check.tool, check.args, got)
		}
	}
	if _, _, err := store.queueSteering(ctx, task.ID, "Leave ticket A open.", "scope-revocation", true); err != nil {
		t.Fatal(err)
	}
	after := currentAutomationEpoch(ctx, endpoint, AutomationControl{})
	if after == "" || after == before {
		t.Fatal("live steering did not invalidate the previous grant epoch")
	}
	call := automationCall{Provider: "playwright", Name: "browser_click", Arguments: json.RawMessage(`{"target":"Archive ticket A"}`)}
	if call.actionAllowed(ctx, endpoint) {
		t.Fatal("the old scope survived a pending caller revocation")
	}
}

func TestAutomationGuardProvidesScreenshotToVerifier(t *testing.T) {
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
seen=false
for arg in "$@"; do
  case "$arg" in --image=*) test -s "${arg#--image=}"; seen=true ;; esac
done
cat >/dev/null
printf '{"type":"item.completed","item":{"type":"agent_message","text":"{\\"allowed\\":%s}"}}\n' "$seen"
`)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 5, 5))); err != nil {
		t.Fatal(err)
	}
	turn := Turn{ExecutionPlan: ExecutionPlan{AutomationWriteScope: "Click Play"}, Agent: Agent{Model: "fixture"}}
	call := automationCall{Provider: "cua", Name: "click", Arguments: json.RawMessage(`{"x":2,"y":2}`)}
	if runtime.authorizeAutomationCall(t.Context(), turn, call) {
		t.Fatal("coordinate action was allowed without evidence")
	}
	call.Capture = &automationCapture{Tool: "get_window_state", Width: 5, Height: 5, Image: data.Bytes()}
	if !runtime.authorizeAutomationCall(t.Context(), turn, call) {
		t.Fatal("the verifier did not receive the screenshot")
	}
}

func TestAutomationGuardCanInspectTabsWithoutWriteAccess(t *testing.T) {
	runtime := codexExecFixture(t, `#!/bin/sh
input=$(cat)
case "$input" in
  *'"mode":"inspection"'*'"browser_request"'*'query { updates { title } }'*) verdict='true,"effect":"read"' ;;
  *'"mode":"inspection"'*'"browser_request"'*) verdict='true,"effect":"change"' ;;
  *'"mode":"inspection"'*'"target":"e2"'*'[ref=e2]'*) verdict=true ;;
  *'"mode":"inspection"'*'"key":"PageDown"'*) verdict=true ;;
  *) verdict=false ;;
esac
printf '{"type":"item.completed","item":{"type":"agent_message","text":"{\\"allowed\\":%s}"}}\n' "$(printf '%s' "$verdict" | sed 's/"/\\"/g')"
`)
	turn := Turn{
		ExecutionPlan: ExecutionPlan{AutomationInspectionScope: "Open the Updates tab in Desk and summarize its contents."},
		Agent:         Agent{Model: "fixture"},
	}
	call := automationCall{Provider: "playwright", Name: "browser_click", Arguments: json.RawMessage(`{"target":"e2"}`)}
	ctx := t.Context()
	if runtime.authorizeAutomationCall(ctx, turn, call) {
		t.Fatal("an inspection click was authorized without observing the control")
	}
	call.Observed = "Desk\ntab \"Updates\" [ref=e2]\nbutton \"Delete\" [ref=e3]"
	if !runtime.authorizeAutomationCall(ctx, turn, call) {
		t.Fatal("the requested read-only tab was blocked")
	}
	call.Request = &browserRequest{URL: "https://desk.example/updates", Method: "POST", Body: "archive=A"}
	if runtime.authorizeAutomationCall(ctx, turn, call) {
		t.Fatal("an inspection click authorized a form submission")
	}
	call.Request = &browserRequest{URL: "https://desk.example/updates", Method: "POST", ContentType: "application/json", Body: `{"query":"query { updates { title } }"}`}
	if !runtime.authorizeAutomationCall(ctx, turn, call) {
		t.Fatal("the verified read-only request was denied")
	}
	delayed := automationCall{Provider: "playwright", Name: "browser_snapshot", Request: call.Request}
	turn.AutomationWriteScope = "Archive ticket A from the Updates tab."
	if !runtime.authorizeAutomationCall(ctx, turn, delayed) {
		t.Fatal("a read-only request between actions was denied")
	}
	delayed.Request = &browserRequest{URL: "https://desk.example/updates", Method: "POST", Body: "archive=A"}
	if runtime.authorizeAutomationCall(ctx, turn, delayed) {
		t.Fatal("a request between actions used the separate write grant")
	}
	turn.AutomationWriteScope = ""
	call.Request = nil
	call.Arguments = json.RawMessage(`{"target":"e3"}`)
	if runtime.authorizeAutomationCall(ctx, turn, call) {
		t.Fatal("inspection granted access to a destructive control")
	}
	call.Name, call.Arguments = "browser_type", json.RawMessage(`{"target":"e2","text":"changed"}`)
	if runtime.authorizeAutomationCall(ctx, turn, call) {
		t.Fatal("inspection granted typing even though the element matched")
	}
	call.Name, call.Arguments = "browser_press_key", json.RawMessage(`{"key":"PageDown"}`)
	if !runtime.authorizeAutomationCall(ctx, turn, call) {
		t.Fatal("inspection denied a scrolling key")
	}
	for _, key := range []string{"Enter", "Control+PageDown", "Delete"} {
		call.Arguments, _ = json.Marshal(map[string]string{"key": key})
		if runtime.authorizeAutomationCall(ctx, turn, call) {
			t.Fatalf("inspection authorized %s", key)
		}
	}
}
