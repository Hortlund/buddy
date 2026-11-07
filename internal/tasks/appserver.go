package tasks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type appFrame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type appTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type appSession struct {
	incoming <-chan appFrame
	input    *os.File
	nextID   int
	events   codexEvents

	threadID        string
	turn            appTurn
	outputSchema    string
	background      bool
	deviceExecution bool
	messageID       string
	messageDraft    string
	messageSent     time.Time
}

func (s *appSession) send(frame map[string]any) error {
	if err := s.input.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	return json.NewEncoder(s.input).Encode(frame)
}

func (s *appSession) call(ctx context.Context, method string, params any, out any) error {
	s.nextID++
	id := strconv.Itoa(s.nextID)
	if err := s.send(map[string]any{"id": s.nextID, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		frame, err := s.receive(ctx)
		if err != nil {
			return err
		}
		if frame.Method == "" && string(frame.ID) == id {
			if frame.Error != nil {
				return errors.New(frame.Error.Message)
			}
			if out != nil {
				return json.Unmarshal(frame.Result, out)
			}
			return nil
		}
		if err := s.handle(frame); err != nil {
			return err
		}
	}
}

func (s *appSession) receive(ctx context.Context) (appFrame, error) {
	select {
	case <-ctx.Done():
		return appFrame{}, ctx.Err()
	case frame, ok := <-s.incoming:
		if !ok {
			return appFrame{}, io.ErrUnexpectedEOF
		}
		return frame, nil
	}
}

func (s *appSession) handle(frame appFrame) error {
	if frame.Method != "" && len(frame.ID) != 0 {
		// The profile is non-interactive. Decline any unexpected request
		// rather than granting permissions or asking the desktop to handle it.
		return s.send(map[string]any{"id": frame.ID, "error": map[string]any{
			"code": -32601, "message": "Buddy does not grant interactive requests",
		}})
	}
	var payload struct {
		ThreadID string  `json:"threadId"`
		ItemID   string  `json:"itemId"`
		Delta    string  `json:"delta"`
		Turn     appTurn `json:"turn"`
		Error    struct {
			Message string `json:"message"`
		} `json:"error"`
		Item struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Text    string `json:"text"`
			Phase   string `json:"phase"`
			Actions []struct {
				Type string `json:"type"`
				Path string `json:"path"`
			} `json:"commandActions"`
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
	}
	switch frame.Method {
	case "item/started", "item/completed", "item/agentMessage/delta", "turn/completed", "error":
		if err := json.Unmarshal(frame.Params, &payload); err != nil {
			return err
		}
	default:
		return nil
	}
	if payload.ThreadID != "" && payload.ThreadID != s.threadID {
		return nil
	}
	switch frame.Method {
	case "turn/completed":
		s.turn = payload.Turn
	case "error":
		s.events.noteError(payload.Error.Message)
	case "item/agentMessage/delta":
		if payload.ItemID != "" && payload.ItemID == s.messageID {
			s.appendMessageDelta(payload.Delta)
		}
	default:
		completed := frame.Method == "item/completed"
		if payload.Item.Type == "agentMessage" {
			if !completed {
				if !s.background && (s.outputSchema == "" || payload.Item.Phase == "commentary") {
					s.messageID, s.messageDraft, s.messageSent = payload.Item.ID, "", time.Time{}
				}
				if payload.Item.Phase != "commentary" {
					s.events.status(Replying)
				}
				return nil
			}
			if s.messageID == payload.Item.ID {
				s.messageID, s.messageDraft = "", ""
			}
			if part := strings.TrimSpace(payload.Item.Text); completed && part != "" {
				if payload.Item.Phase == "commentary" {
					if s.background {
						s.events.publicUpdate("work_update", part)
					} else {
						s.events.commentary(part)
					}
					return nil
				}
				if s.outputSchema == "" && s.events.answer != "" {
					part = s.events.answer + "\n\n" + part
				} else if s.outputSchema == backgroundReplySchema && s.events.answer != "" {
					part = mergeBackgroundReplies(s.events.answer, part)
				}
				s.events.saveAnswer(part)
			}
		} else if payload.Item.Type == "commandExecution" && len(payload.Item.Actions) != 0 {
			kind, detail := Reading, ""
			for _, action := range payload.Item.Actions {
				switch action.Type {
				case "read", "listFiles":
					if detail == "" {
						detail = activityFilename(action.Path)
					}
				case "search":
					kind, detail = Searching, ""
				default:
					kind, detail = RunningTool, ""
				}
				if kind == RunningTool {
					break
				}
			}
			s.events.progress(kind, detail)
		} else if payload.Item.Type == "fileChange" && len(payload.Item.Changes) == 1 {
			s.events.progress(Editing, activityFilename(payload.Item.Changes[0].Path))
		} else {
			s.events.item(payload.Item.Type, payload.Item.Text, completed)
		}
	}
	return nil
}

func mergeBackgroundReplies(previous, current string) string {
	var before, after backgroundReply
	if json.Unmarshal([]byte(previous), &before) != nil || json.Unmarshal([]byte(current), &after) != nil ||
		before.NeedsInput == nil || *before.NeedsInput || after.NeedsInput == nil || strings.TrimSpace(after.Reply) == "" ||
		strings.Contains(after.Reply, before.Reply) {
		return current
	}
	after.Reply = before.Reply + "\n\n" + after.Reply
	data, _ := json.Marshal(after)
	return string(data)
}

func (s *appSession) appendMessageDelta(delta string) {
	// Agent message deltas contain public text. Reasoning deltas are ignored.
	if delta == "" || len(s.messageDraft) > commentaryLimit {
		return
	}
	const writeInterval = 250 * time.Millisecond
	s.messageDraft += delta[:min(len(delta), commentaryLimit+1-len(s.messageDraft))]
	now := time.Now()
	if draft := strings.TrimSpace(s.messageDraft); draft != "" && (now.Sub(s.messageSent) >= writeInterval || len(s.messageDraft) > commentaryLimit) {
		s.events.commentary(draft)
		s.messageSent = now
	}
}

func activityFilename(path string) string {
	name := filepath.Base(path)
	if name == "." || name == ".." || name == string(filepath.Separator) || len(name) > 60 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ""
	}
	return name
}

func textInput(text string) []map[string]string {
	return []map[string]string{{"type": "text", "text": text}}
}

func (s *appSession) initialize(ctx context.Context, key string) error {
	if err := s.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "buddy", "title": "Buddy", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		return err
	}
	if err := s.send(map[string]any{"method": "initialized"}); err != nil || key == "" {
		return err
	}
	if err := s.call(ctx, "account/login/start", map[string]string{"type": "apiKey", "apiKey": key}, nil); err != nil {
		return errors.New("Codex could not use the supplied API key")
	}
	return nil
}

func (s *appSession) start(ctx context.Context, workspace, model, prompt string, images []string) error {
	permissions := "buddy-runtime"
	if s.deviceExecution {
		permissions = "buddy-device"
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := s.call(ctx, "thread/start", map[string]any{
		"cwd": workspace, "model": model, "ephemeral": true, "approvalPolicy": "never", "permissions": permissions,
	}, &thread); err != nil {
		return err
	}
	s.threadID = thread.Thread.ID
	return s.startTurn(ctx, prompt, images)
}

func (s *appSession) startTurn(ctx context.Context, prompt string, images []string) error {
	s.turn = appTurn{}
	s.events.answer = ""
	s.messageID, s.messageDraft, s.messageSent = "", "", time.Time{}
	s.events.status(Listening)
	var start struct {
		Turn appTurn `json:"turn"`
	}
	input := textInput(prompt)
	for _, path := range images {
		input = append(input, map[string]string{"type": "localImage", "path": path})
	}
	params := map[string]any{"threadId": s.threadID, "input": input}
	if s.outputSchema != "" {
		params["outputSchema"] = json.RawMessage(s.outputSchema)
	}
	if err := s.call(ctx, "turn/start", params, &start); err != nil {
		return err
	}
	if !s.done() {
		s.turn = start.Turn
	}
	return nil
}

func (s *appSession) done() bool {
	return s.turn.Status != "" && s.turn.Status != "inProgress"
}

func (s *appSession) run(ctx context.Context, steering *turnSteering) error {
	var wake <-chan struct{}
	if steering != nil && !s.done() {
		steering.ready(true)
		defer steering.ready(false)
		wake = steering.wake
	}
	for !s.done() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
			for !s.done() && ctx.Err() == nil {
				correction, err := steering.next(ctx)
				if err != nil {
					return err
				}
				if correction.ID == 0 {
					break
				}
				var reply struct {
					TurnID string `json:"turnId"`
				}
				delivery, cancel := context.WithTimeout(ctx, 8*time.Second)
				err = s.call(delivery, "turn/steer", map[string]any{
					"threadId": s.threadID, "expectedTurnId": s.turn.ID, "input": textInput(correction.Text),
				}, &reply)
				cancel()
				state := steerApplied
				if err != nil || reply.TurnID != s.turn.ID {
					state = steerMissed
				}
				if err := steering.mark(correction.ID, state); err != nil {
					return err
				}
			}
		case frame, ok := <-s.incoming:
			if !ok {
				return io.ErrUnexpectedEOF
			}
			if err := s.handle(frame); err != nil {
				return err
			}
		}
	}
	if s.turn.Status != "completed" {
		return fmt.Errorf("runner ended the turn with status %s", s.turn.Status)
	}
	return nil
}

func (s *appSession) interrupt() {
	if s.turn.ID == "" || s.done() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.call(ctx, "turn/interrupt", map[string]string{
		"threadId": s.threadID, "turnId": s.turn.ID,
	}, nil)
	for !s.done() {
		frame, err := s.receive(ctx)
		if err != nil || s.handle(frame) != nil {
			return
		}
	}
}

func withAppSession(ctx context.Context, binary, dir, home, key string, flags []string, deviceExecution bool, emit Emit, run func(*appSession) error) error {
	process, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	args := append([]string{binary, "app-server", "--listen", "stdio://"}, flags...)
	args = append(args, "-c", `cli_auth_credentials_store="file"`)
	cmd := command(process, dir, args)
	setCodexCancellation(cmd)
	// This Codex process gets a dedicated CLI home with the existing file
	// login. It never loads the user's other Codex tools, hooks, or config.
	env := cmd.Environ()
	if deviceExecution {
		env = deviceEnvironment(env)
	} else {
		env = slices.DeleteFunc(env, func(v string) bool { return strings.HasPrefix(v, "CODEX_HOME=") })
	}
	cmd.Env = append(env, "CODEX_HOME="+home)
	cmd.Stderr = io.Discard
	childInput, in, err := os.Pipe()
	if err != nil {
		return err
	}
	defer in.Close()
	cmd.Stdin = childInput
	out, err := cmd.StdoutPipe()
	if err != nil {
		childInput.Close()
		return err
	}
	defer out.Close()
	if err := cmd.Start(); err != nil {
		childInput.Close()
		return err
	}
	childInput.Close()
	frames := make(chan appFrame, 32)
	go func() {
		defer close(frames)
		reader := bufio.NewScanner(out)
		reader.Buffer(make([]byte, 64<<10), 8<<20)
		for reader.Scan() {
			var frame appFrame
			if json.Unmarshal(reader.Bytes(), &frame) != nil {
				continue
			}
			select {
			case <-process.Done():
				return
			case frames <- frame:
			}
		}
	}()
	s := appSession{incoming: frames, input: in, events: codexEvents{emit: emit}}
	defer func() {
		if !s.done() {
			s.interrupt()
		}
		in.Close()
		timer := time.AfterFunc(time.Second, cancel)
		_ = cmd.Wait()
		timer.Stop()
	}()
	if err := s.initialize(ctx, key); err != nil {
		return err
	}
	return run(&s)
}

func deviceEnvironment(env []string) []string {
	return slices.DeleteFunc(env, func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TERM", "TMPDIR", "TMP", "TEMP", "TZ",
			"LC_ALL", "LC_CTYPE", "LC_COLLATE", "LC_MESSAGES", "LC_MONETARY", "LC_NUMERIC", "LC_TIME":
			return false
		default:
			return true
		}
	})
}

func runAppServer(ctx context.Context, binary, dir, home, key string, flags []string, turn Turn, emit Emit,
	complete func(modelPass) (string, error), images ...string) (string, error) {
	var reply string
	var completionErr error
	var authError bool
	err := withAppSession(ctx, binary, dir, home, key, flags, turn.DeviceExecution, emit, func(s *appSession) error {
		s.outputSchema, s.background, s.deviceExecution = turn.outputSchema(), turn.Brief != "", turn.DeviceExecution
		s.events.structured = s.outputSchema != ""
		var runnerErr error
		reply, completionErr = complete(func(prompt string) (string, error) {
			if s.threadID == "" {
				runnerErr = s.start(ctx, dir, turn.Agent.Model, prompt, images)
			} else {
				runnerErr = s.startTurn(ctx, prompt, nil)
			}
			if runnerErr == nil {
				runnerErr = s.run(ctx, turn.steering)
			}
			if runnerErr == nil && s.events.answer == "" {
				return "", errors.New("The agent finished without a reply.")
			}
			if runnerErr != nil {
				s.events.noteError(runnerErr.Error())
			}
			return s.events.answer, runnerErr
		})
		authError = s.events.authError
		if completionErr == nil && turn.action != nil && (turn.action.githubResult != nil || turn.action.connectionResult != nil) {
			return nil // A fetched card is useful even if its summary failed.
		}
		return runnerErr
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if key != "" && (authError || strings.Contains(err.Error(), "API key")) {
			return "", errors.New("Codex couldn't authenticate with the OpenAI API key. Check it in Connections.")
		}
		return "", codexFailure(authError)
	}
	return reply, completionErr
}

func isolatedCodexHome() (string, []string, error) {
	current := os.Getenv("CODEX_HOME")
	if current == "" {
		userDir, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		current = filepath.Join(userDir, ".codex")
	}
	current, err := filepath.Abs(current)
	if err != nil {
		return "", nil, err
	}
	auth := filepath.Join(current, "auth.json")
	info, err := os.Stat(auth)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil // Keep the exec path for keyring and other login modes.
	}
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("Codex sign-in is not a regular file")
	}
	dir, err := os.MkdirTemp("", "buddy-codex-")
	if err != nil {
		return "", nil, err
	}
	roots, err := protectedRoots([]string{current, auth, dir})
	if err == nil {
		err = os.Symlink(auth, filepath.Join(dir, "auth.json"))
	}
	if err == nil {
		// Share the CLI's commit lock as well as its file during refresh.
		err = os.Symlink(filepath.Join(current, "auth.lock"), filepath.Join(dir, "auth.lock"))
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, roots, nil
}
