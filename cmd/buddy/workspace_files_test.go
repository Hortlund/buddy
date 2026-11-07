package main

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

func TestConversationFilesTabKeepsDraftAcrossChats(t *testing.T) {
	const firstChat, secondChat = "aster-chat", "birch-chat"
	a := &desktop{conversationID: firstChat, online: true}
	a.directory.Agents = []tasks.AgentRef{
		{ID: "aster", Name: "Aster", ConversationID: firstChat},
		{ID: "birch", Name: "Birch", ConversationID: secondChat},
	}
	a.chat = tasks.Conversation{ID: firstChat, Agent: tasks.Agent{ID: "aster", Name: "Aster"}}
	buffer := editor.NewBuffer("Saved file\n")
	buffer.Set("Unsaved draft\n")
	a.files = map[string]*workspaceFilesPane{
		firstChat: {app: a, relative: "notes.txt", item: tasks.WorkspaceItem{Kind: "text"},
			source: editor.State{Buffer: buffer, Label: "File editor"}},
	}
	view := ui.NewTester(a.view, 820, 590)
	click := func(label string) {
		t.Helper()
		if err := view.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	click("notes.txt")
	if !a.showingWorkspaceFiles() || !view.HasText("Unsaved changes") || !view.HasText("notes.txt") {
		t.Fatalf("Files did not open in the conversation: %v", view.Texts())
	}
	if _, ok := view.Find("Message"); ok {
		t.Fatal("Files still shows the chat composer")
	}
	click("Chat")
	if _, ok := view.Find("Message"); !ok || a.showingWorkspaceFiles() {
		t.Fatal("Chat did not restore its composer")
	}
	click("Birch")
	if _, ok := view.Find("Chat"); ok || a.currentConversation() != secondChat || view.HasText("notes.txt") {
		t.Fatalf("a chat without an open file displayed tabs: %v", view.Texts())
	}
	if !a.hasWorkspaceEdits() {
		t.Fatal("unsaved changes in the hidden tab would be missed on close")
	}
	click("Aster")
	click("notes.txt")
	if !view.HasText("Unsaved changes") || a.currentWorkspaceFiles().source.Buffer.String() != "Unsaved draft\n" {
		t.Fatal("switching conversations lost the file draft")
	}
	buffer.MarkSaved()
	view.Frame()
	click("Close notes.txt")
	if _, ok := view.Find("Message"); !ok || a.currentWorkspaceFiles() != nil || a.showingWorkspaceFiles() {
		t.Fatal("closing the file tab did not restore the chat")
	}
}

func TestWorkspaceFilesReleaseHiddenPreviewAndRejectLateLoad(t *testing.T) {
	a := &desktop{conversationID: "chat", conversationTab: conversationFilesTab}
	picture := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &workspaceFilesPane{app: a, relative: "photo.png", item: tasks.WorkspaceItem{Kind: "image"},
		image: picture, loading: true, request: 5, cancelLoad: cancel}
	a.files = map[string]*workspaceFilesPane{"chat": b}
	a.syncWorkspaceFiles()
	a.conversationTab = conversationChatTab
	a.syncWorkspaceFiles()
	if a.visibleFiles != nil || b.image != nil || b.relative != "photo.png" || !b.refreshOnShow || b.loading || ctx.Err() == nil {
		t.Fatalf("leaving Files kept its preview or lost its location: %+v", b)
	}
	b.finishLoad(5, tasks.WorkspaceItem{Kind: "image"}, picture, nil)
	if b.image != nil {
		t.Fatal("a late image load repopulated the hidden pane")
	}
	a.conversationTab = conversationFilesTab
	a.syncWorkspaceFiles()
	b.finishLoad(b.request, tasks.WorkspaceItem{Kind: "image"}, picture, nil)
	if b.image != picture {
		t.Fatal("the visible pane did not accept a fresh preview")
	}
}

func TestWorkspaceRefreshRetainsEditorForUnchangedText(t *testing.T) {
	a := &desktop{conversationID: "chat", conversationTab: conversationFilesTab}
	buffer := editor.NewBuffer("original")
	b := &workspaceFilesPane{app: a, relative: "notes.md", preview: false, request: 4,
		item: tasks.WorkspaceItem{Kind: "text", Text: "original", Revision: "v1"}, source: editor.State{Buffer: buffer}}
	a.files = map[string]*workspaceFilesPane{"chat": b}
	b.finishLoad(4, tasks.WorkspaceItem{Kind: "text", Text: "original", Revision: "v2"}, nil, nil)
	if b.source.Buffer != buffer || b.item.Revision != "v2" || b.preview {
		t.Fatal("refreshing unchanged text reset the editor or kept its old revision")
	}
	b.finishLoad(4, tasks.WorkspaceItem{Kind: "text", Text: "updated", Revision: "v3"}, nil, nil)
	if b.source.Buffer.String() != "updated" || b.dirty() {
		t.Fatal("refreshed text did not replace the clean buffer")
	}
}

func TestWorkspaceCloseWaitsForSaves(t *testing.T) {
	for _, outcome := range []string{"saved", "failed", "keep open"} {
		t.Run(outcome, func(t *testing.T) {
			a := &desktop{}
			first, second := &workspaceFilesPane{saving: true}, &workspaceFilesPane{saving: true}
			a.files = map[string]*workspaceFilesPane{"first": first, "second": second}
			continued := 0
			a.afterWorkspaceDiscard(func() { continued++ })
			view := ui.NewTester(a.view, 820, 590)
			view.Advance(250 * time.Millisecond)
			if continued != 0 || !view.HasText("Saving workspace files…") {
				t.Fatal("a hidden save did not keep Buddy open with a status")
			}
			first.saving = false
			a.workspaceSaveFinished("first.md", nil)
			if continued != 0 {
				t.Fatal("Buddy closed before the other save finished")
			}
			var err error
			switch outcome {
			case "failed":
				err = errors.New("test save failure")
				second.source.Buffer = editor.NewBuffer("old")
				second.source.Buffer.Set("draft")
			case "keep open":
				if err := view.Click("Keep open"); err != nil {
					t.Fatal(err)
				}
			}
			second.saving = false
			a.workspaceSaveFinished("second.md", err)
			view.Frame()
			if outcome == "saved" && continued != 1 || outcome != "saved" && continued != 0 || view.HasText("Saving workspace files…") {
				t.Fatalf("unexpected close state after %s: continued=%d, view=%v", outcome, continued, view.Texts())
			}
			if outcome == "failed" && (!second.dirty() || !view.HasText("Couldn’t save second.md") ||
				!view.HasText("Your edits are still in the “second.md” tab in its chat.")) {
				t.Fatal("a failed save lost the draft or hid the error")
			}
		})
	}
}
