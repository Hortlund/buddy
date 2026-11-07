package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceEditPreservesDarwinACL(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const name = "notes.txt"
	if err := root.WriteFile(name, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	changeACL := func(entry string) {
		t.Helper()
		if out, err := exec.Command("/bin/chmod", "+a", entry, filepath.Join(dir, name)).CombinedOutput(); err != nil {
			t.Fatalf("set ACL: %v: %s", err, out)
		}
	}
	acl := func(file string) string {
		t.Helper()
		out, err := exec.Command("/bin/ls", "-le", filepath.Join(dir, file)).Output()
		if err != nil {
			t.Fatal(err)
		}
		_, entries, _ := strings.Cut(string(out), "\n")
		return entries
	}
	changeACL("everyone deny execute")
	before := acl(name)
	if !strings.Contains(before, "everyone deny execute") {
		t.Fatalf("expected ACL missing: %q", before)
	}
	info, err := root.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stageWorkspaceEdit(root, name, "changed", info)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Remove(staged)
	if got := acl(staged); got != before {
		t.Fatalf("ACL changed from %q to %q", before, got)
	}
	changeACL("everyone deny write")
	info, err = root.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageWorkspaceEdit(root, name, "should not write", info); err == nil {
		t.Fatal("edit bypassed a deny-write ACL")
	}

	if out, err := exec.Command("/bin/chmod", "+a", "everyone deny execute,file_inherit,only_inherit", dir).CombinedOutput(); err != nil {
		t.Fatalf("set inherited ACL: %v: %s", err, out)
	}
	const plain = "plain.txt"
	if err := root.WriteFile(plain, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(acl(plain), "inherited deny execute") {
		t.Fatal("test file did not inherit the directory ACL")
	}
	if out, err := exec.Command("/bin/chmod", "-N", filepath.Join(dir, plain)).CombinedOutput(); err != nil {
		t.Fatalf("remove inherited ACL: %v: %s", err, out)
	}
	info, err = root.Stat(plain)
	if err != nil {
		t.Fatal(err)
	}
	staged, err = stageWorkspaceEdit(root, plain, "changed", info)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Remove(staged)
	if got := acl(staged); got != acl(plain) {
		t.Fatalf("save introduced an inherited ACL: %q", got)
	}
}
