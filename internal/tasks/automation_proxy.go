package tasks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

type ExecutionFrame struct {
	Data      []byte    `json:"data,omitempty"`
	MediaType string    `json:"media_type,omitempty"`
	Provider  string    `json:"provider"`
	At        time.Time `json:"at"`
	Paused    bool      `json:"paused"`
}

type automationContent struct {
	Type  string `json:"type"`
	Data  string `json:"data,omitempty"`
	Media string `json:"mimeType,omitempty"`
	Text  string `json:"text,omitempty"`
}

type automationResult struct {
	ID     json.RawMessage `json:"id"`
	Error  json.RawMessage `json:"error"`
	Result struct {
		IsError bool                `json:"isError,omitempty"`
		Content []automationContent `json:"content"`
	} `json:"result"`
}

type automationMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params automationCall  `json:"params"`
}

func automationRequestKey(id json.RawMessage) string {
	if len(id) != 0 && id[0] == '"' {
		var value string
		if json.Unmarshal(id, &value) == nil {
			canonical, _ := json.Marshal(value)
			return string(canonical)
		}
	}
	return string(id)
}

func (r *CodexRuntime) frameDirectory(job string) (string, error) {
	if !mcpName.MatchString(job) {
		return "", ErrNotFound
	}
	dir := filepath.Join(r.Key.Dir, "execution-frames", job)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(r.Key.Dir)
	if err != nil {
		return "", err
	}
	if real != filepath.Join(root, "execution-frames", job) {
		return "", &InputError{"The execution frame directory changed."}
	}
	return real, nil
}

func (r *CodexRuntime) ExecutionFrame(job string) (ExecutionFrame, error) {
	var frame ExecutionFrame
	dir, err := r.frameDirectory(job)
	if err != nil {
		return frame, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "frame.json"))
	if errors.Is(err, os.ErrNotExist) {
		return frame, nil
	}
	if err != nil || len(data) > 6<<20 {
		return frame, err
	}
	err = json.Unmarshal(data, &frame)
	return frame, err
}

func (r *CodexRuntime) ApplyAutomationControl(job string, control AutomationControl) error {
	dir, err := r.frameDirectory(job)
	if err != nil {
		return err
	}
	r.automationMu.Lock()
	defer r.automationMu.Unlock()
	current, err := readAutomationControlFile(dir)
	if err != nil {
		return err
	}
	if current == control || current.Revision > control.Revision {
		return nil
	}
	data, _ := json.Marshal(control)
	return writePrivateExecution(filepath.Join(dir, "control.json"), data)
}

func readAutomationControlFile(dir string) (AutomationControl, error) {
	var control AutomationControl
	data, err := os.ReadFile(filepath.Join(dir, "control.json"))
	if errors.Is(err, os.ErrNotExist) {
		_, err := os.Stat(filepath.Join(dir, "paused"))
		if errors.Is(err, os.ErrNotExist) {
			return control, nil
		}
		control.Paused = err == nil
		return control, err
	}
	if err != nil {
		return control, err
	}
	err = json.Unmarshal(data, &control)
	return control, err
}

// The proxy enforces the job's tool permissions, records returned screenshots,
// and holds future UI tool calls while a human takes over.
func RunAutomationProxy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("automation-proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("frames", "", "private frame directory")
	provider := flags.String("provider", "", "provider name")
	guard := flags.String("guard", "", "job automation authorization endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) == 0 || !filepath.IsAbs(command[0]) || !filepath.IsAbs(*dir) {
		return errors.New("Invalid automation proxy configuration")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var browser *browserScope
	if *provider == "playwright" {
		var err error
		browser, command, err = startBrowserScope(ctx, *dir, *guard, command)
		if err != nil {
			return err
		}
		defer browser.close()
	}
	child := exec.CommandContext(ctx, command[0], command[1:]...)
	if browser != nil {
		// The recorded launch arguments and Buddy's flags define the managed session.
		child.Env = browserProcessEnv(os.Environ())
	}
	setProcessGroup(child)
	child.Stderr = os.Stderr
	input, err := child.StdinPipe()
	if err != nil {
		return err
	}
	output, err := child.StdoutPipe()
	if err != nil {
		return err
	}
	if err = child.Start(); err != nil {
		return err
	}
	var inputMu sync.Mutex
	writeProvider := func(line []byte) error {
		inputMu.Lock()
		defer inputMu.Unlock()
		_, err := input.Write(append(line, '\n'))
		return err
	}
	var replyMu sync.Mutex
	pending := make(map[string]chan []byte)
	writeLine := func(line []byte) {
		if _, err := os.Stdout.Write(append(line, '\n')); err != nil {
			cancel()
		}
	}
	reject := func(id json.RawMessage, reason string) {
		result := map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": reason}}}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		writeLine(data)
	}
	exchange := func(ctx context.Context, id json.RawMessage, line []byte, send func([]byte) error) (automationResult, []byte) {
		key := automationRequestKey(id)
		reply := make(chan []byte, 1)
		replyMu.Lock()
		pending[key] = reply
		replyMu.Unlock()
		defer func() {
			replyMu.Lock()
			delete(pending, key)
			replyMu.Unlock()
		}()
		var result automationResult
		if err := send(line); err != nil {
			if ctx.Err() == nil {
				cancel()
			}
			return result, nil
		}
		select {
		case body := <-reply:
			if json.Unmarshal(body, &result) == nil {
				return result, body
			}
		case <-ctx.Done():
		}
		return result, nil
	}
	// Keep provider-generated evidence separate from the model's responses.
	observe := func(ctx context.Context, tool string, args json.RawMessage) (automationResult, bool) {
		id, _ := json.Marshal("buddy-private-" + rand.Text())
		line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		query, done := context.WithTimeout(ctx, 12*time.Second)
		defer done()
		result, body := exchange(query, id, line, writeProvider)
		return result, body != nil && len(result.Error) == 0 && !result.Result.IsError
	}
	const refresh = "Automation control changed. Observe the current page or window and choose the action again."
	type toolRequest struct {
		message automationMessage
		line    []byte
		ctx     context.Context
		cancel  context.CancelFunc
		sent    bool
	}
	var toolsMu sync.Mutex
	registered := make(map[string]*toolRequest)
	forget := func(request *toolRequest) {
		toolsMu.Lock()
		defer toolsMu.Unlock()
		delete(registered, automationRequestKey(request.message.ID))
		request.cancel()
	}
	tools := make(chan *toolRequest, 32)
	go func() {
		defer input.Close()
		defer cancel()
		defer close(tools)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			var message automationMessage
			if json.Unmarshal(line, &message) != nil || strings.HasPrefix(automationRequestKey(message.ID), `"buddy-private-`) {
				continue
			}
			if message.Method == "notifications/cancelled" {
				var notification struct {
					Params struct {
						RequestID json.RawMessage `json:"requestId"`
					}
				}
				_ = json.Unmarshal(line, &notification)
				toolsMu.Lock()
				request := registered[automationRequestKey(notification.Params.RequestID)]
				if request != nil {
					request.cancel()
				}
				forward := request == nil || request.sent
				var err error
				if forward {
					err = writeProvider(line)
				}
				toolsMu.Unlock()
				if err != nil {
					return
				}
				continue
			}
			if message.Method != "tools/call" {
				if err := writeProvider(line); err != nil {
					return
				}
				continue
			}
			if len(message.ID) == 0 {
				continue
			}
			callCtx, stop := context.WithCancel(ctx)
			request := &toolRequest{message: message, line: line, ctx: callCtx, cancel: stop}
			toolsMu.Lock()
			key := automationRequestKey(message.ID)
			if registered[key] != nil {
				toolsMu.Unlock()
				stop()
				continue
			}
			registered[key] = request
			toolsMu.Unlock()
			select {
			case tools <- request:
			case <-ctx.Done():
				forget(request)
				return
			default:
				forget(request)
				reject(message.ID, "Too many automation calls are pending. Retry after the current calls complete.")
			}
		}
	}()
	go func() {
		var observed struct {
			revision int64
			text     string
			capture  *automationCapture
			fresh    bool
		}
		process := func(request *toolRequest) {
			defer forget(request)
			ctx := request.ctx
			if ctx.Err() != nil {
				return
			}
			observe := func(tool string, args json.RawMessage) (automationResult, bool) {
				return observe(ctx, tool, args)
			}
			message, line := request.message, request.line
			call := message.Params
			call.Provider = *provider
			call.Request = nil
			action := automationActionTool(call.Provider, call.Name)
			if !action && !call.inspectionAllowed() {
				reject(message.ID, "This automation call falls outside the job's authorized operations.")
				return
			}
			if browser != nil && call.Name == "browser_take_screenshot" {
				// The browser uses CSS pixels for its mouse coordinates.
				var args map[string]json.RawMessage
				_ = json.Unmarshal(call.Arguments, &args)
				if args == nil {
					args = make(map[string]json.RawMessage)
				}
				args["scale"] = json.RawMessage(`"css"`)
				call.Arguments, _ = json.Marshal(args)
				message.Params = automationCall{Name: call.Name, Arguments: call.Arguments}
				line, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "method": message.Method, "params": message.Params})
			}
			control, err := readAutomationControlFile(*dir)
			wasPaused := control.Paused
			for err == nil && control.Paused {
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
				control, err = readAutomationControlFile(*dir)
			}
			if err != nil {
				reject(message.ID, "Buddy couldn't verify automation control.")
				return
			}
			if observed.revision != control.Revision || wasPaused {
				observed.revision, observed.text, observed.capture, observed.fresh = control.Revision, "", nil, false
			}
			call.Revision, call.Observed, call.Capture = control.Revision, observed.text, nil
			if call.usesCoordinates() {
				call.Capture = observed.capture
			}
			if action && (wasPaused || control.Revision != 0 && !observed.fresh) {
				reject(message.ID, refresh)
				return
			}
			if browser != nil && action && !browser.verifyView(ctx, *dir, *guard, control, observe) {
				reject(message.ID, "The current browser view is outside the job's authorized scope.")
				return
			}
			var elementEvidence []string
			var click string
			var clickArguments json.RawMessage
			if browser != nil && action {
				var ok bool
				elementEvidence, ok = browserElementEvidence(call, observe)
				if !ok {
					reject(message.ID, "The browser couldn't verify the current target. Take a fresh snapshot and choose the action again.")
					return
				}
				if len(elementEvidence) != 0 {
					const limit = 24 << 10
					call.Observed += "\n\n" + strings.Join(elementEvidence, "\n\n")
					call.Observed = call.Observed[max(0, len(call.Observed)-limit):]
				}
				if call.Name == "browser_click" {
					click = rand.Text()
					clickArguments, ok = prepareBrowserClick(call.Arguments, elementEvidence, click, observe)
					if !ok {
						reject(message.ID, "Buddy couldn't bind the browser click to the verified element. Take a fresh snapshot and choose the action again.")
						return
					}
				}
			}
			var epoch string
			if call.requiresScope() {
				var allowed bool
				epoch, allowed = call.authorize(ctx, *guard)
				if !allowed {
					reject(message.ID, "This automation call falls outside the job's authorized operations or needs a fresh observation.")
					return
				}
			}
			if len(elementEvidence) != 0 {
				freshEvidence, ok := browserElementEvidence(call, observe)
				if !ok || !slices.Equal(elementEvidence, freshEvidence) {
					observed.capture, observed.text, observed.fresh = nil, "", false
					reject(message.ID, "The browser target changed. Take a fresh snapshot and choose the action again.")
					return
				}
			}
			if call.usesCoordinates() && !verifyAutomationPixels(call.Capture, observe) {
				observed.capture, observed.text, observed.fresh = nil, "", false
				reject(message.ID, "The screen changed. Take a fresh screenshot and choose the action again.")
				return
			}
			latest, err := readAutomationControlFile(*dir)
			if err != nil || latest != control {
				reject(message.ID, refresh)
				return
			}
			if browser != nil && call.requiresScope() && (epoch == "" || epoch != currentAutomationEpoch(ctx, *guard, control)) {
				reject(message.ID, refresh)
				return
			}
			if browser != nil && !call.requiresScope() && call.Name != "browser_close" {
				epoch = currentAutomationEpoch(ctx, *guard, control)
			}
			if browser != nil {
				browser.noteHistoryEpoch(epoch)
			}
			if browser != nil && (call.Name == "browser_network_requests" || call.Name == "browser_console_messages") {
				if !browser.verifyView(ctx, *dir, *guard, control, observe) {
					reject(message.ID, "The current browser view is outside the job's authorized scope.")
					return
				}
				diagnostics, ok := browser.diagnostics(call, epoch, observe)
				if !ok || ctx.Err() != nil || currentAutomationEpoch(ctx, *guard, control) != epoch {
					reject(message.ID, "Buddy couldn't verify the current browser diagnostics.")
					return
				}
				result := map[string]any{"content": []automationContent{{Type: "text", Text: diagnostics}}}
				data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
				writeLine(data)
				return
			}
			if browser != nil && call.Name == "browser_navigate" {
				var target struct{ URL string }
				_ = json.Unmarshal(call.Arguments, &target)
				browser.allowNavigation(target.URL, epoch)
			}
			if action || call.Name == "launch_app" || call.Name == "browser_navigate" || call.Name == "browser_navigate_back" || call.Name == "browser_tabs" {
				observed.capture, observed.text, observed.fresh = nil, "", false
			}
			if click != "" {
				line, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "method": "tools/call",
					"params": map[string]any{"name": "browser_run_code_unsafe", "arguments": clickArguments}})
			}
			tracksBrowser := browser != nil && call.Name != "browser_close"
			if tracksBrowser {
				browser.beginAction(ctx, call, epoch, click)
			}
			result, body := exchange(ctx, message.ID, line, func(line []byte) error {
				toolsMu.Lock()
				defer toolsMu.Unlock()
				if err := ctx.Err(); err != nil {
					return err
				}
				err := writeProvider(line)
				request.sent = err == nil
				return err
			})
			var denials []string
			if tracksBrowser {
				denials = browser.finishAction()
			}
			if body == nil || ctx.Err() != nil {
				return
			}
			if tracksBrowser && call.Name != "browser_tabs" && len(result.Error) == 0 &&
				!browser.verifyView(ctx, *dir, *guard, control, observe) {
				reject(message.ID, "The resulting browser view is outside the job's authorized scope. Navigate to an authorized page.")
				return
			}
			if browser != nil && len(result.Error) == 0 {
				for i := range result.Result.Content {
					result.Result.Content[i].Text = stripBrowserTabSection(result.Result.Content[i].Text)
				}
				if click != "" {
					description := "Clicked the verified browser target."
					if result.Result.IsError {
						description = "The verified browser click did not complete. Take a fresh snapshot before retrying."
					}
					result.Result.Content = []automationContent{{Type: "text", Text: description}}
				}
				if call.Name == "browser_tabs" && !result.Result.IsError {
					if len(result.Result.Content) == 0 || !strings.Contains(result.Result.Content[0].Text, "### Result\nNo open tabs.") {
						tabs, ok := browser.tabSummary(ctx, *dir, *guard, control, observe)
						if !ok {
							reject(message.ID, "Buddy couldn't verify the current browser tabs.")
							return
						}
						result.Result.Content = []automationContent{{Type: "text", Text: tabs}}
					}
				}
				if len(denials) > 0 {
					result.Result.Content = append(result.Result.Content, automationContent{Type: "text",
						Text: "Buddy blocked requests outside this action's authorization scope (" + strings.Join(denials, ", ") + ")."})
				}
				body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result.Result})
			}
			latest, err = readAutomationControlFile(*dir)
			if err != nil || latest != control {
				reject(message.ID, refresh)
				return
			}
			observeTarget := len(result.Error) == 0 && !result.Result.IsError && observesAutomationTarget(call.Name)
			if observeTarget {
				observed.capture = nil
			}
			for _, content := range result.Result.Content {
				switch content.Type {
				case "text":
					if result.Result.IsError || content.Text == "" {
						continue
					}
					const limit = 24 << 10
					observed.text += "\n\n" + content.Text[:min(len(content.Text), limit)]
					observed.text = observed.text[max(0, len(observed.text)-limit):]
					observed.fresh = observed.fresh || observeTarget
				case "image":
					data, err := base64.StdEncoding.DecodeString(content.Data)
					if err != nil || !validExecutionImage(data) || result.Result.IsError {
						continue
					}
					if observeTarget {
						cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
						observed.capture = &automationCapture{Tool: call.Name, Arguments: call.Arguments, Width: cfg.Width, Height: cfg.Height, Image: data}
						observed.fresh = true
					}
					frame := ExecutionFrame{Data: data, MediaType: content.Media, Provider: *provider, At: time.Now().UTC()}
					encoded, _ := json.Marshal(frame)
					_ = writePrivateExecution(filepath.Join(*dir, "frame.json"), encoded)
				}
			}
			writeLine(body)
		}
		for request := range tools {
			process(request)
		}
	}()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		var message automationMessage
		if json.Unmarshal(line, &message) == nil && message.Method == "" {
			key := automationRequestKey(message.ID)
			replyMu.Lock()
			reply := pending[key]
			delete(pending, key)
			replyMu.Unlock()
			if reply != nil {
				reply <- line
				continue
			}
			if strings.HasPrefix(key, `"buddy-private-`) {
				continue
			}
		}
		writeLine(line)
	}
	cancel()
	waitErr := child.Wait()
	if scanner.Err() != nil {
		return scanner.Err()
	}
	return waitErr
}

func verifyAutomationPixels(capture *automationCapture, observe func(string, json.RawMessage) (automationResult, bool)) bool {
	if capture == nil {
		return false
	}
	switch capture.Tool {
	case "browser_take_screenshot", "get_window_state", "get_desktop_state":
	default:
		return false
	}
	if capture.Tool == "browser_take_screenshot" {
		var args map[string]json.RawMessage
		_ = json.Unmarshal(capture.Arguments, &args)
		if args["element"] != nil || args["ref"] != nil || args["target"] != nil ||
			string(args["fullPage"]) == "true" || string(args["scale"]) == `"device"` {
			return false
		}
	}
	result, ok := observe(capture.Tool, capture.Arguments)
	if !ok {
		return false
	}
	for _, part := range result.Result.Content {
		if part.Type != "image" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(part.Data)
		return err == nil && bytes.Equal(capture.Image, data)
	}
	return false
}

func browserElementEvidence(call automationCall, observe func(string, json.RawMessage) (automationResult, bool)) ([]string, bool) {
	if call.usesCoordinates() {
		return nil, true
	}
	var args struct {
		Target      string
		StartTarget string
		EndTarget   string
		Fields      []struct{ Target string }
	}
	if len(call.Arguments) != 0 && json.Unmarshal(call.Arguments, &args) != nil {
		return nil, false
	}
	targets := []string{args.Target, args.StartTarget, args.EndTarget}
	for _, field := range args.Fields {
		targets = append(targets, field.Target)
	}
	targets = slices.DeleteFunc(targets, func(target string) bool { return target == "" })
	if len(targets) != 0 || call.Name == "browser_press_key" || call.Name == "browser_mouse_wheel" {
		// A target can keep its label while the record surrounding it changes.
		targets = append([]string{""}, targets...)
	}
	evidence := make([]string, 0, len(targets))
	for _, target := range targets {
		var snapshotArgs json.RawMessage = []byte(`{}`)
		if target != "" {
			snapshotArgs, _ = json.Marshal(map[string]string{"target": target})
		}
		result, ok := observe("browser_snapshot", snapshotArgs)
		if !ok || len(result.Result.Content) == 0 || result.Result.Content[0].Text == "" {
			return nil, false
		}
		evidence = append(evidence, stripBrowserTabSection(result.Result.Content[0].Text))
	}
	return evidence, true
}

var clickElementReference = regexp.MustCompile(`\[ref=((?:f\d+)?e\d+)\](?:\s*:|$)`)
var bareElementReference = regexp.MustCompile(`^(?:f\d+)?e\d+$`)

func prepareBrowserClick(args json.RawMessage, evidence []string, token string, observe func(string, json.RawMessage) (automationResult, bool)) (json.RawMessage, bool) {
	var input struct {
		Target      string
		Button      string
		DoubleClick bool
		Modifiers   []string
	}
	if json.Unmarshal(args, &input) != nil || input.Target == "" || len(evidence) == 0 {
		return nil, false
	}
	target := input.Target
	if !bareElementReference.MatchString(target) {
		_, snapshot, _ := strings.Cut(evidence[len(evidence)-1], "### Snapshot\n```yaml\n")
		line, _, _ := strings.Cut(snapshot, "\n")
		if reference := clickElementReference.FindStringSubmatch(line); len(reference) == 2 {
			target = reference[1]
		}
	}
	type clickCommand struct {
		Stage   string         `json:"stage"`
		Token   string         `json:"token"`
		Target  string         `json:"target,omitempty"`
		Options map[string]any `json:"options,omitempty"`
	}
	argsFor := func(command clickCommand) json.RawMessage {
		data, _ := json.Marshal(command)
		code := "async page => { if (!await page[Symbol.for('buddy.guardedClick')](" + string(data) +
			")) throw new Error('The verified browser click expired.'); return true; }"
		args, _ := json.Marshal(map[string]string{"code": code})
		return args
	}
	_, ok := observe("browser_run_code_unsafe", argsFor(clickCommand{Stage: "prepare", Token: token, Target: target}))
	options := make(map[string]any)
	if input.Button != "" {
		options["button"] = input.Button
	}
	if input.DoubleClick {
		options["clickCount"] = 2
	}
	if len(input.Modifiers) > 0 {
		options["modifiers"] = input.Modifiers
	}
	return argsFor(clickCommand{Stage: "click", Token: token, Options: options}), ok
}

func stripBrowserTabSection(text string) string {
	var kept []string
	skip := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "### ") {
			skip = line == "### Open tabs"
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func validExecutionImage(data []byte) bool {
	if len(data) == 0 || len(data) > MaxWorkArtifactBytes {
		return false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && (format == "png" || format == "jpeg") && cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= 10000 && cfg.Height <= 10000 && int64(cfg.Width)*int64(cfg.Height) <= 25_000_000
}

func observesAutomationTarget(tool string) bool {
	switch tool {
	case "browser_navigate", "browser_navigate_back", "browser_snapshot", "browser_take_screenshot", "get_window_state", "get_desktop_state", "get_accessibility_tree", "zoom":
		return true
	}
	return false
}
