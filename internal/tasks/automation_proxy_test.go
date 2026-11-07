package tasks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
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
				ID int `json:"id"`
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
	cmd := exec.CommandContext(ctx, exe, "-test.run=TestAutomationProxyHelper", "--", "-frames", dir, "-provider", "cua", "--", driver)
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
}
