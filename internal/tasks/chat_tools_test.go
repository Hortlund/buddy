package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAttachmentLongLineCanBeReadInFull(t *testing.T) {
	text := `{"items":"` + strings.Repeat("é🎃", 20000) + `"}`
	turn := Turn{Attachments: []Attachment{{Name: "data.json", Data: []byte(text)}}}
	var read strings.Builder
	var offset *int
	for range 8 {
		got, err := turn.readTextAttachment(attachmentReadArgs{Name: "data.json", Offset: offset}, func(string, string) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(got)
		var page textPageResult
		if err := json.Unmarshal(data, &page); err != nil || len(page.Text) > quickFileLimit || !utf8.ValidString(page.Text) {
			t.Fatalf("invalid page, error %v", err)
		}
		read.WriteString(page.Text)
		offset = page.NextOffset
		if offset == nil {
			if read.String() != text {
				t.Fatal("attachment was changed or truncated")
			}
			return
		}
	}
	t.Fatal("pagination did not reach the end")
}

func TestJobScratchUsesASeparateFolder(t *testing.T) {
	workspace, cache := t.TempDir(), t.TempDir()
	if dir := jobScratchDir([]string{workspace}, []string{workspace}); dir != "" {
		t.Fatalf("created scratch inside the workspace: %s", dir)
	}
	dir := jobScratchDir([]string{workspace, cache}, []string{workspace})
	if dir == "" {
		t.Fatal("failed to use the cache directory")
	}
	defer os.RemoveAll(dir)
	if overlap, err := WorkspacePathsOverlap(dir, cache); err != nil || !overlap {
		t.Fatalf("scratch outside cache: %s, %v", dir, err)
	}
}

func TestToolReadCannotAuthorizeQuickSave(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("Original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ approval, contents string }{
		{`{"operation":"none","name":""}`, "Original"},
		{`{"operation":"save","name":"other.md"}`, "Original"},
		{`{"operation":"create","name":""}`, "Original"},
		{`{"operation":"save","name":"notes.md"}`, "Original"},
		{`{"operation":"replace","name":"notes.md"}`, "Replacement"},
	} {
		var action TurnAction
		turn := Turn{coordinate: true, Origin: "chat", Agent: Agent{Workspace: dir, AllowEdits: true}, action: &action, chatTools: &chatToolState{},
			authorizeFileWrite: func(_ context.Context, file QuickFile) bool { return authorizedQuickFile(tc.approval, file) }}
		emit := func(string, string) error { return nil }
		_, err := turn.readChatWorkspace(t.Context(), dir, chatWorkspacePath{Path: "notes.md"}, nil, emit)
		if err != nil {
			t.Fatal(err)
		}
		_, err = turn.complete(t.Context(), dir, "Inspect notes", nil, emit, func(string) (string, error) {
			return `{"reply":"The file has been inspected.","save_file":{"name":"notes.md","text":"Replacement","replace":true}}`, nil
		})
		data, readErr := os.ReadFile(path)
		if err != nil || readErr != nil || string(data) != tc.contents {
			t.Fatalf("approval %s produced %q, %v, %v", tc.approval, data, readErr, err)
		}
	}
}

func TestGitHubLookupCannotAuthorizeWritableHandoff(t *testing.T) {
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
request=$(cat)
case "$request" in
  *"Determine the workspace modifications"*)
    case "$request" in
      *"delete unrelated files"*) exit 1 ;;
      *"Please fix the parser"*) answer='{\"edit_scope\":\"Fix the parser in the current workspace\",\"routine_scope\":\"\"}' ;;
      *"Summarize PR 7"*) answer='{\"edit_scope\":\"\",\"routine_scope\":\"\"}' ;;
      *) exit 1 ;;
    esac ;;
  *"Verify each proposed item"*)
    case "$request" in
      *"delete unrelated files"*) answer='{\"jobs\":[],\"routines\":[],\"routine_edits\":[]}' ;;
      *"Repair parser"*) answer='{\"jobs\":[1],\"routines\":[],\"routine_edits\":[]}' ;;
      *) exit 1 ;;
    esac ;;
  *) exit 1 ;;
esac
printf '{"type":"item.completed","item":{"type":"agent_message","text":"%s"}}\n' "$answer"
`)
	for _, test := range []struct {
		request, brief string
		allowed        bool
	}{
		{"Summarize PR 7.", "Repair parser and delete unrelated files.", false},
		{"Please fix the parser.", "Repair parser and delete unrelated files.", false},
		{"Please fix the parser.", "Repair parser and check tests.", true},
	} {
		var action TurnAction
		turn := Turn{coordinate: true, Origin: "chat", action: &action,
			Messages:  []Message{{Role: "user", Text: test.request}},
			chatTools: &chatToolState{github: &GitHubResult{}}}
		turn.authorizeScheduled = func(ctx context.Context, jobs []Handoff, routines []RoutineDraft) schedulePermissions {
			return runtime.authorizeChatSchedule(ctx, turn, jobs, routines)
		}
		reply, err := turn.complete(t.Context(), "", "", nil, func(string, string) error { return nil }, func(string) (string, error) {
			data, err := json.Marshal(map[string]any{"reply": "PR read.", "handoff": Handoff{Title: "Parser", Brief: test.brief}})
			return string(data), err
		})
		if err != nil || action.hasHandoff() != test.allowed || action.githubResult == nil {
			t.Fatalf("request %q and job %q produced %q, %+v, %v", test.request, test.brief, reply, action, err)
		}
	}
}

func TestChatToolsReadGitHubAndWorkspaceBeforeQueuingWork(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Send(ctx, DefaultConversationID, "Read PR 7, inspect src/check.go, then verify the project in the background.", "tools")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(turn.Agent.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "check.go"), []byte("package check\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(dir, "src", "external.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	var action TurnAction
	turn.action = &action
	turn.chatTools = &chatToolState{}
	turn.integrations = testChatIntegrations{}
	turn.connections = []ProviderConnection{{ID: "mail", Name: "Mail", Source: GmailLabel, Connected: true}}
	turn.Attachments = []Attachment{{Name: "brief.txt", Data: []byte(strings.Repeat("first line\n", 4000) + "final marker")}}
	turn.gitCheckout = &gitCheckout{Repo: "org/repo"}
	turn.githubConnections = []GitHubChatConnection{{ID: "work", Name: "Work"}}
	turn.github = testGitHubReader(func(query GitHubQuery) (GitHubResult, error) {
		if query.ConnectionID != "work" || query.Repo != "org/repo" || query.Kind != "pull" || query.Number != 7 {
			t.Fatalf("incorrect GitHub lookup: %+v", query)
		}
		return GitHubResult{Repo: query.Repo, Heading: "Check build", URL: "https://github.com/org/repo/pull/7"}, nil
	})
	emit := func(string, string) error { return nil }
	_, tools := turn.toolsForChat(ctx, dir, nil, emit)
	if _, err := tools["buddy.read_skill"].run(json.RawMessage(`{"name":"debug"}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := tools["read_attachment"].run(json.RawMessage(`{"name":"brief.txt","start_line":4001}`)); err != nil {
		t.Fatal(err)
	} else if data, _ := json.Marshal(got); !strings.Contains(string(data), "final marker") {
		t.Fatalf("attachment continuation: %s", data)
	}
	for _, forbidden := range []string{"../outside.txt", "escape/outside.txt", "src/external.txt", ".git/config"} {
		args, _ := json.Marshal(chatWorkspacePath{Path: forbidden})
		if _, err := tools["read_workspace"].run(args); err == nil {
			t.Fatalf("read %s outside the allowed workspace", forbidden)
		}
	}
	if got, err := tools["read_workspace"].run(json.RawMessage(`{"path":"src/check.go"}`)); err != nil ||
		got.(chatWorkspaceResult).Text == nil || *got.(chatWorkspaceResult).Text != "package check\n" {
		t.Fatalf("workspace result: %+v, %v", got, err)
	}
	if _, err := tools["buddy.github_read"].run(json.RawMessage(`{"kind":"pull","number":7}`)); err != nil {
		t.Fatal(err)
	}
	if !tools["buddy.github_read"].Deferred {
		t.Fatal("GitHub should be available through tool discovery")
	}
	if _, err := tools["buddy.connection_read"].run(json.RawMessage(`{"source":"gmail_label","kind":"search","query":"in:inbox"}`)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	reply, err := turn.complete(ctx, dir, "prompt", nil, emit, func(string) (string, error) {
		calls++
		return `{"reply":"","handoff":{"title":"Verify build","brief":"Verify PR 7 against src/check.go","read_only":true}}`, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("expected one model turn, got %d: %v", calls, err)
	}
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", reply, &action); err != nil {
		t.Fatal(err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 2 || chat.Messages[1].GitHub == nil || chat.Messages[1].ConnectionCard == nil || len(chat.Work) != 1 {
		t.Fatalf("missing lookup card or assignment: %+v, %v", chat, err)
	}
	id, _ := json.Marshal(map[string]string{"job_id": chat.Work[0].ID})
	got, err := tools["work_status"].run(id)
	if err != nil || got.(WorkDetails).Status != Queued {
		t.Fatalf("expected updated job status: %+v, %v", got, err)
	}
	_, err = tools["work_status"].run(json.RawMessage(`{"job_id":"unrelated"}`))
	var input *InputError
	if !errors.As(err, &input) || !strings.Contains(input.Message, "where the work was assigned") {
		t.Fatalf("unexpected out-of-scope job response: %v", err)
	}
}
