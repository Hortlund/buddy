package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestMCPManagementUsesOwnerAccessAndRedactsBearerTokens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(t.Context(), filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime, err := tasks.NewCodexRuntime("/missing/codex", "", localconfig.CodexKey{Dir: dir}, dir)
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.EnrollDevice(t.Context(), "Worker")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler("owner-token", store, nil, runtime)
	call := func(method, path, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+auth)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	body := `{"name":"test","transport":"http","url":"https://tools.example/mcp","token":"secret-fixture"}`
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if out := call(method, mcpPath, device.Token, body); out.Code != http.StatusUnauthorized {
			t.Fatalf("worker accessed owner settings: %d", out.Code)
		}
	}
	created := call(http.MethodPost, mcpPath, "owner-token", body)
	var server tasks.MCPServer
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &server) != nil || !server.HasToken {
		t.Fatalf("save failed with status %d", created.Code)
	}
	list := call(http.MethodGet, mcpPath, "owner-token", "")
	for _, out := range []*httptest.ResponseRecorder{created, list} {
		if bytes.Contains(out.Body.Bytes(), []byte("secret-fixture")) {
			t.Fatal("MCP response leaked a bearer token")
		}
	}
	if out := call(http.MethodDelete, mcpPath+"/"+server.ID, "owner-token", ""); out.Code != http.StatusNoContent {
		t.Fatalf("delete failed with status %d", out.Code)
	}
	if out := call(http.MethodGet, mcpPath, "owner-token", ""); out.Body.String() != "[]\n" {
		t.Fatalf("deleted server remained in the catalog: %s", out.Body.String())
	}
}
