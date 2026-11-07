//go:build darwin || linux

package tasks

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestCancelStopsDetachedCodexTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := command(ctx, t.TempDir(), []string{os.Args[0], "-test.run=^TestCodexProcessFixture$"})
	cmd.Env = append(cmd.Environ(), "BUDDY_CANCEL_FIXTURE=runner")
	setCodexCancellation(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(stdout)
	if !scan.Scan() {
		cancel()
		_ = cmd.Wait()
		t.Fatal("fixture did not return a tool PID")
	}
	pid, err := strconv.Atoi(scan.Text())
	if err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	_ = cmd.Wait()
	live, err := liveProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if _, alive := live[pid]; alive {
		t.Fatalf("detached command %d still running after cancellation", pid)
	}
}

func TestCodexProcessFixture(t *testing.T) {
	switch os.Getenv("BUDDY_CANCEL_FIXTURE") {
	case "runner":
		tool := exec.Command(os.Args[0], "-test.run=^TestCodexProcessFixture$")
		tool.Env = append(os.Environ(), "BUDDY_CANCEL_FIXTURE=tool")
		tool.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := tool.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Println(tool.Process.Pid)
		_ = tool.Wait()
	case "tool":
		time.Sleep(12 * time.Second)
	}
}
