package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestCodexKeyIsPrivateAndNeverReturned(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	dir := t.TempDir()
	key := localconfig.CodexKey{Dir: dir}
	runtime := &tasks.CodexRuntime{Key: key}
	handler := Handler("fixture-token", nil, nil, runtime)
	const secret = "sk-test-placeholder-0000"
	request := func(method, body string, authenticated bool, wantCode int, wantSource string) {
		t.Helper()
		req := httptest.NewRequest(method, codexKeyPath, strings.NewReader(body))
		if authenticated {
			req.Header.Set("Authorization", "Bearer fixture-token")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != wantCode || strings.Contains(w.Body.String(), secret) {
			t.Fatalf("%s returned status %d or leaked the credential", method, w.Code)
		}
		if wantSource != "" {
			var auth CodexAuth
			if err := json.Unmarshal(w.Body.Bytes(), &auth); err != nil || auth.Source != wantSource {
				t.Fatalf("%s auth source = %q, %v", method, auth.Source, err)
			}
		}
	}
	request(http.MethodPut, `{"key":"`+secret+`"}`, false, http.StatusUnauthorized, "")
	request(http.MethodPut, `{"key":"`+secret+`"}`, true, http.StatusOK, "saved_key")
	info, err := os.Stat(filepath.Join(dir, "codex.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("API key file was unavailable or not private: %v", err)
	}
	if got, _, err := key.Read(); err != nil || got != secret {
		t.Fatal("saved API key could not be loaded")
	}
	request(http.MethodGet, "", true, http.StatusOK, "saved_key")
	request(http.MethodDelete, "", true, http.StatusOK, "cli")
}
