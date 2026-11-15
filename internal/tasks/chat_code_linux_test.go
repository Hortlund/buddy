package tasks

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNativeChatCodeSandbox(t *testing.T) {
	if os.Getenv("BUDDY_TEST_DELEGATED_CGROUP") != "1" {
		t.Skip("Requires a delegated Linux service cgroup")
	}
	if err := PrepareChatCodeSandbox(); err != nil {
		t.Fatal(err)
	}
	runtimes := nativeChatCodeRuntimes()
	if len(runtimes) != 2 {
		t.Fatalf("Expected native Go and Python, got %v", runtimes)
	}
	t.Run("short memory spike", TestChatCodeRejectsShortMemorySpike)
	t.Run("memory limit", TestInlinePythonStopsAtMemoryBudget)
	t.Run("process limit", TestChatCodeCapsProcesses)
	t.Run("cancellation", TestInlinePythonStopsOnCancellation)
	t.Run("detached child", TestInlinePythonCannotLeaveDetachedChild)
	t.Run("read-only files", TestInlinePythonCannotWriteFiles)
	t.Run("Go chat run", TestChatCodeRunsInlineAndKeepsHostFilesPrivate)

	sandbox, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	result := runChatCode(t.Context(), sandbox, runtimes["python"], `import pathlib
for line in pathlib.Path('/proc/self/cgroup').read_text().splitlines():
    if line.startswith('0::'):
        group = pathlib.Path('/sys/fs/cgroup') / line[3:].lstrip('/')
        try:
            (group / 'memory.max').write_text('max')
        except OSError:
            print('blocked')
`, nil)
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "blocked\n" {
		t.Fatalf("snippet could modify its cgroup: %+v", result)
	}
	entries, err := os.ReadDir(runtimes["python"].cgroup)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "buddy-snippet-") {
			t.Fatalf("run left behind a cgroup: %s", entry.Name())
		}
	}
}
