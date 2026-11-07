package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"

	"golang.org/x/sys/unix"
)

func TestWorkspaceFileEditingProtectsConcurrentChanges(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	token, err := testToken(t, state)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(state, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := tasks.NewCodexRuntime(filepath.Join(root, "no-codex"), "", localconfig.CodexKey{Dir: state}, state)
	if err != nil {
		t.Fatal(err)
	}
	workspaces := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaces, 0700); err != nil {
		t.Fatal(err)
	}
	if err := runtime.EnableWorkspaces(ctx, store, workspaces); err != nil {
		t.Fatal(err)
	}
	id := tasks.DefaultAgentID
	agent, err := store.Agent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(agent.Workspace, "notes")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	relative := "notes/plan + review.md"
	filename := filepath.Join(agent.Workspace, relative)
	if err := os.WriteFile(filename, []byte("# First\r\n"), 0640); err != nil {
		t.Fatal(err)
	}
	const attr = "user.buddy.review"
	attrValue := []byte("keep this file metadata")
	if err := unix.Setxattr(filename, attr, attrValue, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(state, filepath.Join(folder, "outside")); err != nil {
		t.Fatal(err)
	}
	handler := Handler(token, store, tasks.NewWorker(store, nil, nil), runtime)
	origin, _, err := store.Send(ctx, tasks.DefaultConversationID, "Look at the plan", "reference")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, pair.Code, "Reference preview phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, workspaceItemURL(id, "", relative), http.StatusOK},
		{http.MethodGet, workspaceItemURL(id, "", relative) + "&task=" + origin.ID, http.StatusOK},
		{http.MethodGet, workspaceItemURL(id, "", relative) + "&task=tsk_unknown", http.StatusBadRequest},
		{http.MethodGet, workspaceItemURL(id, "", "../state/buddy.db"), http.StatusBadRequest},
		{http.MethodPut, workspaceItemURL(id, "", relative), http.StatusForbidden},
	} {
		req := httptest.NewRequest(check.method, check.path, nil)
		req.Header.Set("Authorization", "Bearer "+phone.Token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != check.status {
			t.Fatalf("phone reference access %s %s: %d %s", check.method, check.path, response.Code, response.Body.String())
		}
	}
	server := startTestServer(t, state, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workspace/item") && r.URL.Query().Get("path") == relative {
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
				t.Error(err)
				return
			}
			w = delayedAttachmentWriter{w}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(state, localconfig.TokenName))
	defer client.Close()

	list, err := client.WorkspaceItem(ctx, id, agent.Workspace, "notes")
	if err != nil || list.Kind != "folder" || len(list.Entries) != 1 || list.Entries[0].Name != filepath.Base(filename) {
		t.Fatalf("unexpected folder contents: %+v, %v", list, err)
	}
	for _, invalid := range []string{"../state/buddy.db", "notes/.private", "notes/outside/buddy.db"} {
		if _, err := client.WorkspaceItem(ctx, id, agent.Workspace, invalid); err == nil {
			t.Fatalf("opened forbidden path %q", invalid)
		}
	}
	item, err := client.WorkspaceItem(ctx, id, agent.Workspace, relative)
	if err != nil || item.Kind != "text" || item.Text != "# First\r\n" || item.Revision == "" || item.ReadOnly {
		t.Fatalf("could not open text: %+v, %v", item, err)
	}
	bound, err := client.WorkspaceItem(ctx, id, agent.Workspace, relative, origin.ID)
	if err != nil || bound.SourceTask != origin.ID || bound.Text != item.Text {
		t.Fatalf("server did not verify the reference binding: %+v, %v", bound, err)
	}
	var remote *HTTPError
	for _, body := range []map[string]any{
		{"revision": item.Revision},
		{"revision": item.Revision, "text": nil},
	} {
		endpoint := workspaceItemURL(id, agent.Workspace, relative)
		if err := client.call(ctx, http.MethodPut, endpoint, "", body, nil); !errors.As(err, &remote) || remote.Status != http.StatusBadRequest {
			t.Fatalf("accepted a missing or null text field: %v", err)
		}
	}
	edit := tasks.WorkspaceFileEdit{Revision: item.Revision, Text: "# Saved\r\n"}
	revision, err := client.SaveWorkspaceFile(ctx, id, agent.Workspace, relative, edit)
	if err != nil || revision == item.Revision {
		t.Fatalf("save did not return a new revision: %q, %v", revision, err)
	}
	if repeated, err := client.SaveWorkspaceFile(ctx, id, agent.Workspace, relative, edit); err != nil || repeated != revision {
		t.Fatalf("save retry did not reuse the new revision: %q, %v", repeated, err)
	}
	if saved, err := os.ReadFile(filename); err != nil || string(saved) != edit.Text {
		t.Fatalf("saved file = %q, %v", saved, err)
	}
	info, err := os.Stat(filename)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("save did not preserve permissions: %v, %v", info, err)
	}
	attrData := make([]byte, len(attrValue))
	if n, err := unix.Getxattr(filename, attr, attrData); err != nil || string(attrData[:n]) != string(attrValue) {
		t.Fatalf("save did not preserve extended attributes: %v", err)
	}
	if err := os.WriteFile(filename, []byte("# Edited elsewhere\r\n"), 0640); err != nil {
		t.Fatal(err)
	}
	edit = tasks.WorkspaceFileEdit{Revision: revision, Text: "# Stale edit\r\n"}
	if _, err := client.SaveWorkspaceFile(ctx, id, agent.Workspace, relative, edit); !errors.As(err, &remote) || remote.Status != http.StatusConflict {
		t.Fatalf("stale edit didn't conflict: %v", err)
	}
	if saved, err := os.ReadFile(filename); err != nil || string(saved) != "# Edited elsewhere\r\n" {
		t.Fatalf("concurrent edit was lost: %q, %v", saved, err)
	}
	if err := os.Chmod(filename, 0444); err != nil {
		t.Fatal(err)
	}
	item, err = client.WorkspaceItem(ctx, id, agent.Workspace, relative)
	if err != nil || !item.ReadOnly {
		t.Fatalf("read-only file opened for editing: %+v, %v", item, err)
	}
	edit = tasks.WorkspaceFileEdit{Revision: item.Revision, Text: "overwrite"}
	if _, err := client.SaveWorkspaceFile(ctx, id, agent.Workspace, relative, edit); !errors.As(err, &remote) || remote.Status != http.StatusBadRequest {
		t.Fatalf("read-only file was writable: %v", err)
	}
	moved := filepath.Join(root, "other-workspace")
	if err := os.MkdirAll(filepath.Join(moved, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moved, relative), []byte("other file"), 0600); err != nil {
		t.Fatal(err)
	}
	oldWorkspace := agent.Workspace
	agent.Workspace = moved
	if _, err := store.SaveAgent(ctx, id, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WorkspaceItem(ctx, id, oldWorkspace, relative); !errors.As(err, &remote) || remote.Status != http.StatusConflict {
		t.Fatalf("reloading followed the changed workspace: %v", err)
	}
	if _, err := client.WorkspaceItem(ctx, id, "", relative, origin.ID); !errors.As(err, &remote) || remote.Status != http.StatusConflict {
		t.Fatalf("task-bound reference followed the changed workspace: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, workspaceItemURL(id, "", relative)+"&task="+origin.ID, nil)
	request.Header.Set("Authorization", "Bearer "+phone.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("phone followed a stale file reference: %d %s", response.Code, response.Body)
	}
	if _, err := client.SaveWorkspaceFile(ctx, id, oldWorkspace, relative, edit); !errors.As(err, &remote) || remote.Status != http.StatusConflict {
		t.Fatalf("saving followed the changed workspace: %v", err)
	}
	item, err = client.WorkspaceItem(ctx, id, moved, relative)
	if err != nil || item.Text != "other file" {
		t.Fatalf("could not reopen the new workspace: %+v, %v", item, err)
	}
	if _, err := client.SaveWorkspaceFile(ctx, id, moved, relative, tasks.WorkspaceFileEdit{Revision: item.Revision, Text: ""}); err != nil {
		t.Fatalf("explicit empty text could not be saved: %v", err)
	}
}

func TestWorkspaceFileReferenceRequiresBindingSupport(t *testing.T) {
	dir := t.TempDir()
	if _, err := testToken(t, dir); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, tasks.WorkspaceItem{Kind: "text", Text: "unverified file"})
	}))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()
	if _, err := client.WorkspaceItem(t.Context(), tasks.DefaultAgentID, "", "plan.md", "tsk_origin"); err == nil {
		t.Fatal("silently opened a reference on a server that ignored its binding")
	}
	if _, err := client.WorkspaceItem(t.Context(), tasks.DefaultAgentID, "", "plan.md"); err != nil {
		t.Fatalf("ordinary workspace browsing lost compatibility: %v", err)
	}
}
