package references

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceLinksRoundTripAndRejectEscapes(t *testing.T) {
	for _, relative := range []string{".", "notes/book list + résumé.md", "src/server.go", "a%20b.md"} {
		link := File("agt_orbit", relative)
		if strings.Contains(link, "+") {
			t.Fatalf("file URL uses form-only space encoding: %q", link)
		}
		target, ok := Parse(link)
		if !ok || target.Kind != "files" || target.ID != "agt_orbit" || target.Path != relative {
			t.Fatalf("file link did not round-trip: %q => %+v, %v", link, target, ok)
		}
	}
	if target, ok := Parse(Work("conv_team", "tsk_work")); !ok || target.ID != "conv_team" || target.Job != "tsk_work" {
		t.Fatalf("job link did not round-trip: %+v, %v", target, ok)
	}
	for _, raw := range []string{
		"buddy://files/agt_orbit?path=../secret", "buddy://files/agt_orbit?path=/etc/passwd",
		"buddy://files/agt_orbit?path=.env", "buddy://files/agt_orbit?path=a%5Csecret",
		"buddy://files/agt_orbit?path=C%3Asecret", "buddy://files/user/other?path=ok.txt",
		"buddy://owner@files/agt_orbit?path=ok.txt", "buddy://work/conv_team?job=../other",
		"buddy://execute/agt_orbit?path=ok.txt", "javascript:alert(1)", "file:///etc/passwd",
	} {
		if target, ok := Parse(raw); ok {
			t.Fatalf("accepted unsafe or unsupported reference %q: %+v", raw, target)
		}
	}
}

func TestLinkFilesKeepsCodeAndExistingLinksIntact(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"book list.md", "main.go", ".env"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	text := "Saved `book list.md` and `" + filepath.Join(root, "main.go") + "`.\n" +
		"[`main.go`](https://example.com/code) and `missing.md` and `.env`.\n" +
		"```go\n`main.go`\n```\n~~~text\n`book list.md`\n~~~\n"
	got := LinkFiles(text, "agt_orbit", root)
	if !strings.Contains(got, "[`book list.md`]("+File("agt_orbit", "book list.md")+")") ||
		!strings.Contains(got, File("agt_orbit", "main.go")) {
		t.Fatalf("existing relative or absolute file wasn't linked: %s", got)
	}
	unchanged := text[strings.Index(text, "[`main.go`]"):]
	if !strings.HasSuffix(got, unchanged) {
		t.Fatalf("code, existing links, missing files, or hidden files changed: %s", got)
	}
}
