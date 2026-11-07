package tasks

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
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
	"time"
)

type ExecutionFrame struct {
	Data      []byte    `json:"data,omitempty"`
	MediaType string    `json:"media_type,omitempty"`
	Provider  string    `json:"provider"`
	At        time.Time `json:"at"`
	Paused    bool      `json:"paused"`
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

func (r *CodexRuntime) PauseAutomation(job string, paused bool) error {
	dir, err := r.frameDirectory(job)
	if err != nil {
		return err
	}
	file := filepath.Join(dir, "paused")
	if paused {
		return os.WriteFile(file, []byte("paused"), 0600)
	}
	err = os.Remove(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// The proxy forwards the provider's MCP protocol unchanged. It records only
// returned screenshots, and holds future UI tool calls while a human takes over.
func RunAutomationProxy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("automation-proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("frames", "", "private frame directory")
	provider := flags.String("provider", "", "provider name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) == 0 || !filepath.IsAbs(command[0]) || !filepath.IsAbs(*dir) {
		return errors.New("Invalid automation proxy configuration")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	child := exec.CommandContext(ctx, command[0], command[1:]...)
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
	go func() {
		defer input.Close()
		defer cancel()
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			var message struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(line, &message)
			if message.Method == "tools/call" {
				for {
					if _, err := os.Stat(filepath.Join(*dir, "paused")); errors.Is(err, os.ErrNotExist) {
						break
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
			if _, err := input.Write(append(line, '\n')); err != nil {
				return
			}
		}
	}()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		var message struct {
			Result struct {
				Content []struct {
					Type  string `json:"type"`
					Data  string `json:"data"`
					Media string `json:"mimeType"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &message) == nil {
			for _, content := range message.Result.Content {
				if content.Type != "image" {
					continue
				}
				data, err := base64.StdEncoding.DecodeString(content.Data)
				if err != nil || !validExecutionImage(data) {
					continue
				}
				frame := ExecutionFrame{Data: data, MediaType: content.Media, Provider: *provider, At: time.Now().UTC()}
				encoded, _ := json.Marshal(frame)
				_ = writePrivateExecution(filepath.Join(*dir, "frame.json"), encoded)
			}
		}
		if _, err = os.Stdout.Write(append(bytes.Clone(line), '\n')); err != nil {
			cancel()
			break
		}
	}
	cancel()
	waitErr := child.Wait()
	if scanner.Err() != nil {
		return scanner.Err()
	}
	return waitErr
}

func validExecutionImage(data []byte) bool {
	if len(data) == 0 || len(data) > MaxWorkArtifactBytes {
		return false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && (format == "png" || format == "jpeg") && cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= 10000 && cfg.Height <= 10000 && int64(cfg.Width)*int64(cfg.Height) <= 25_000_000
}

func (s *Store) AutomationPaused(ctx context.Context, job string) (bool, error) {
	var paused bool
	err := s.db.QueryRowContext(ctx, "SELECT paused FROM automation_control WHERE task_id=?", job).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return paused, err
}
