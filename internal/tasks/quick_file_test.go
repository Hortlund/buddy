package tasks

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestQuickFileCompletesInChatWithoutAHandoff(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnableWorkspaces(ctx, root); err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Send(ctx, DefaultConversationID, "Recommend books and save them in book file.md.", "book")
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	var action TurnAction
	turn.action = &action
	const answer = `{"reply":"Try **Piranesi**.","handoff":null,"routine":null,"card":null,
		"save_file":{"name":"book file.md","text":"# Recommendations\n\n- Piranesi\n"}}`
	reply, err := turn.complete(ctx, turn.Agent.Workspace, "test request", nil, func(string, string) error { return nil }, func(string) (string, error) {
		return answer, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", reply, &action); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(turn.Agent.Workspace, "book file.md"))
	if err != nil || string(data) != "# Recommendations\n\n- Piranesi\n" {
		t.Fatalf("saved recommendations: %q, %v", data, err)
	}
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Work) != 0 || len(chat.Messages) != 2 ||
		!strings.Contains(chat.Messages[1].Text, "Try **Piranesi**.") || !strings.Contains(chat.Messages[1].Text, "I saved it as **book file.md**") {
		t.Fatalf("quick save was not a direct chat result: %+v, %v", chat, err)
	}

	task, _, err = store.Send(ctx, DefaultConversationID, "Can you update the book list with new books?", "update-book")
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.turn(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	turn.action = &action
	var notes []string
	calls := 0
	reply, err = turn.complete(ctx, turn.Agent.Workspace, "test follow-up", nil, func(stream, text string) error {
		if stream == "commentary" {
			notes = append(notes, text)
		}
		return nil
	}, func(prompt string) (string, error) {
		calls++
		if calls == 1 {
			return `{"reply":"","read_file":{"name":"book file.md","save_as":"book file.md"}}`, nil
		}
		if !strings.Contains(prompt, `# Recommendations\n\n- Piranesi\n`) {
			t.Fatalf("model didn't receive the existing file: %s", prompt)
		}
		return `{"reply":"Added **The Dispossessed**.","save_file":{"name":"book file.md","text":"# Recommendations\n\n- Piranesi\n- The Dispossessed\n","replace":true}}`, nil
	})
	if err != nil || calls != 2 || !slices.Equal(notes, []string{"Reading book file.md…", "Reviewing book file.md…", "Saving book file.md…"}) {
		t.Fatalf("inline update calls=%d, notes=%q, err=%v", calls, notes, err)
	}
	if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", reply, &action); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(turn.Agent.Workspace, "book file.md"))
	if err != nil || string(data) != "# Recommendations\n\n- Piranesi\n- The Dispossessed\n" {
		t.Fatalf("updated recommendations: %q, %v", data, err)
	}
	chat, err = store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Work) != 0 || len(chat.Messages) != 4 ||
		!strings.Contains(chat.Messages[3].Text, "Added **The Dispossessed**.") || !strings.Contains(chat.Messages[3].Text, "I updated **book file.md**") {
		t.Fatalf("quick update was not a direct chat result: %+v, %v", chat, err)
	}
}

func TestQuickFileCannotOverwriteOrLeaveTheWorkspace(t *testing.T) {
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "notes.md")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../notes.md", "notes.md", ".git/config", "main.go"} {
		if _, err := saveQuickFile(ctx, root, QuickFile{Name: name, Text: "changed"}, nil, nil); err == nil {
			t.Fatalf("unsafe quick save accepted: %s", name)
		}
	}
	if _, err := saveQuickFile(ctx, root, QuickFile{Name: "other.md", Text: "changed"}, []string{root}, nil); err == nil {
		t.Fatal("a protected workspace accepted a quick save")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "original" {
		t.Fatalf("the original file changed: %q, %v", data, err)
	}
}

func TestQuickFileUpdateRequiresUnchangedSnapshot(t *testing.T) {
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "notes.md")
	if err := os.WriteFile(name, []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	update := QuickFile{Name: "notes.md", Text: "updated", Replace: true}
	if _, err := saveQuickFile(ctx, root, update, nil, nil); err == nil {
		t.Fatal("updated without a snapshot")
	}
	turn := Turn{Agent: Agent{Workspace: root}}
	original, err := turn.readQuickFile(ctx, root, update.Name, nil, func(string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("changed elsewhere"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := saveQuickFile(ctx, root, update, nil, original); err == nil {
		t.Fatal("overwrote a concurrent change")
	}
	if data, err := os.ReadFile(name); err != nil || string(data) != "changed elsewhere" {
		t.Fatalf("the concurrent edit was lost: %q, %v", data, err)
	}
}
