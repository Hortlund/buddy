package tasks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const codexExecEndpointEnv = "CODEX_EXEC_SERVER_URL"
const codexFrameLimit = 8 << 20

// Codex exec's JSONL stream exposes command start and completion. The executor
// carries live output, keyed by process ID. Codex still launches the command
// and sends the same sandbox settings to its own executor.
func serveCodexExecProgress(ctx context.Context, binary, dir string, emit Emit) (string, func() bool, func(), error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	home, err := os.MkdirTemp("", "buddy-codex-executor-")
	if err != nil {
		_ = listener.Close()
		return "", nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	path := "/" + rand.Text() + rand.Text()
	progress := &execCommandProgress{emit: emit, pending: make(map[string]execCommandStart), runs: make(map[string]*execProcessRun)}
	var connected atomic.Bool
	var connection sync.Mutex
	closed := false
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Path), []byte(path)) != 1 || connected.Load() {
			http.NotFound(w, r)
			return
		}
		connection.Lock()
		defer connection.Unlock()
		if closed || connected.Load() {
			http.NotFound(w, r)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		socket.SetReadLimit(codexFrameLimit)
		// Only one connection can claim the endpoint, before any tool runs.
		connected.Store(true)
		relayCodexExecutor(ctx, binary, dir, home, socket, progress)
	})}
	go func() { _ = server.Serve(listener) }()
	closeProgress := func() {
		cancel()
		_ = server.Close()
		connection.Lock()
		defer connection.Unlock()
		closed = true
		_ = os.RemoveAll(home)
	}
	return "ws://" + listener.Addr().String() + path, connected.Load, closeProgress, nil
}

func relayCodexExecutor(ctx context.Context, binary, dir, home string, socket *websocket.Conn, progress *execCommandProgress) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := command(ctx, dir, []string{binary, "exec-server", "--listen", "stdio://"})
	// The executor needs runtime variables and PATH but no personal Codex
	// configuration. In particular, its environment config RPC must not load
	// the keyring user's MCP configuration into the isolated turn.
	cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
	setCodexCancellation(cmd)
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		defer cancel()
		defer input.Close()
		for {
			kind, data, err := socket.Read(ctx)
			if err != nil || kind != websocket.MessageText {
				return
			}
			progress.request(data)
			// Each WebSocket message is one JSON value. Compact it so
			// pretty-printed JSON cannot split into multiple stdio frames.
			var line bytes.Buffer
			if json.Compact(&line, data) != nil {
				return
			}
			line.WriteByte('\n')
			if _, err := input.Write(line.Bytes()); err != nil {
				return
			}
		}
	})
	workers.Go(func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				progress.flush()
			}
		}
	})
	lines := bufio.NewScanner(output)
	lines.Buffer(make([]byte, 64<<10), codexFrameLimit)
	for lines.Scan() {
		data := lines.Bytes()
		progress.response(data)
		if err := socket.Write(ctx, websocket.MessageText, data); err != nil {
			break
		}
	}
	cancel()
	_ = input.Close()
	_ = output.Close()
	_ = cmd.Wait()
	workers.Wait()
	progress.flush()
}

type execCommandStart struct {
	ProcessID string `json:"processId"`
	Argv      []string
	Metadata  struct {
		ToolCallID string `json:"toolCallId"`
	}
}

type execCommandProgress struct {
	mu      sync.Mutex
	emit    Emit
	pending map[string]execCommandStart
	runs    map[string]*execProcessRun
}

type execProcessRun struct {
	result ChatCodeRun
	stdout codeOutput
	stderr codeOutput
	dirty  bool
}

func (p *execCommandProgress) request(data []byte) {
	var request struct {
		ID     json.RawMessage
		Method string
		Params execCommandStart
	}
	if json.Unmarshal(data, &request) != nil || request.Method != "process/start" ||
		request.Params.Metadata.ToolCallID == "" || request.Params.ProcessID == "" || len(request.Params.Argv) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending[string(request.ID)] = request.Params
}

func (p *execCommandProgress) response(data []byte) {
	var response struct {
		ID     json.RawMessage
		Method string
		Result struct {
			ProcessID string `json:"processId"`
		}
		Params struct {
			ProcessID     string `json:"processId"`
			Stream        string
			Chunk         []byte
			ExitCode      *int `json:"exitCode"`
			SandboxDenied bool `json:"sandboxDenied"`
		}
	}
	if json.Unmarshal(data, &response) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if start, ok := p.pending[string(response.ID)]; ok {
		delete(p.pending, string(response.ID))
		if response.Result.ProcessID == start.ProcessID {
			run := &execProcessRun{result: ChatCodeRun{RunID: "run_" + rand.Text(), WorkCommand: true, Status: Running,
				StartedAt: time.Now().UTC(), chatCodeResult: chatCodeResult{Command: boundedCommand(displayExecCommand(start.Argv))}}}
			p.runs[start.ProcessID] = run
			p.publish(run)
		}
		return
	}
	id := response.Params.ProcessID
	run := p.runs[id]
	if run == nil {
		return
	}
	switch response.Method {
	case "process/output":
		if response.Params.Stream == "stderr" {
			_, _ = run.stderr.Write(response.Params.Chunk)
		} else {
			_, _ = run.stdout.Write(response.Params.Chunk)
		}
		run.dirty = true
		if run.result.Stdout == "" && run.result.Stderr == "" {
			p.publish(run)
		}
	case "process/exited":
		finished := time.Now().UTC()
		run.result.Status, run.result.FinishedAt, run.result.ExitCode = Failed, &finished, response.Params.ExitCode
		if response.Params.SandboxDenied {
			run.result.Error = "The command was blocked by the workspace sandbox."
		} else if run.result.ExitCode != nil && *run.result.ExitCode == 0 {
			run.result.Status = Succeeded
		}
		if p.publish(run) {
			delete(p.runs, id)
		}
	}
}

func (p *execCommandProgress) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, run := range p.runs {
		if run.dirty && p.publish(run) && run.result.FinishedAt != nil {
			delete(p.runs, id)
		}
	}
}

func (p *execCommandProgress) publish(run *execProcessRun) bool {
	run.result.Stdout, run.result.Stderr = run.stdout.String(), run.stderr.String()
	run.result.Truncated = run.stdout.truncated || run.stderr.truncated
	run.dirty = publishCodeRun(p.emit, run.result) != nil
	return !run.dirty
}

func displayExecCommand(argv []string) string {
	args := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == "" || strings.ContainsAny(arg, " \t\r\n\"'\\;|&<>$()") {
			arg = strconv.Quote(arg)
		}
		args = append(args, arg)
	}
	return strings.Join(args, " ")
}
