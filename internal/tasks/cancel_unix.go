//go:build darwin || linux

package tasks

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type childProcess struct {
	pid, parent int
	started     string
}

func liveProcesses() (map[int]childProcess, error) {
	out, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,stat=,lstart=").Output()
	if err != nil {
		return nil, err
	}
	list := make(map[int]childProcess)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 8 || strings.HasPrefix(fields[2], "Z") {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr == nil && parentErr == nil && pid > 1 {
			list[pid] = childProcess{pid, parent, strings.Join(fields[3:], " ")}
		}
	}
	return list, nil
}

func collectChildren(pid int, list map[int]childProcess, out map[int]childProcess) {
	for _, process := range list {
		if process.parent == pid {
			out[process.pid] = process
			collectChildren(process.pid, list, out)
		}
	}
}

func setCodexCancellation(cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		list, err := liveProcesses()
		if err != nil {
			// Give Codex a chance to clean up even if ps is unavailable.
			return cmd.Process.Signal(os.Interrupt)
		}
		pid := cmd.Process.Pid
		children := make(map[int]childProcess)
		collectChildren(pid, list, children)

		// Codex handles SIGINT as turn/interrupt and waits for its detached
		// tool sessions. Keep track of those sessions for the fallback.
		if err := cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			current, err := liveProcesses()
			if err != nil {
				return err
			}
			if runner, ok := current[pid]; ok && runner == list[pid] {
				collectChildren(pid, current, children)
			}
			for child, previous := range children {
				if actual, ok := current[child]; !ok || actual.started != previous.started {
					delete(children, child)
				}
			}
			if len(children) == 0 {
				return nil
			}
			if time.Now().After(deadline) {
				var killErr error
				for child := range children {
					if err := syscall.Kill(child, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
						killErr = errors.Join(killErr, err)
					}
				}
				return killErr
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}
