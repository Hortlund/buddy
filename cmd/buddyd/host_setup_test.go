package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"buddy/internal/localconfig"
)

func TestHostHelpersPreservePrivateAccessAndReportSignIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cert, err := localconfig.EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	token, err := localconfig.EnsureToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	var state atomic.Value
	state.Store("sign_in_required")
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			fmt.Fprint(w, `{"service":"buddy","desktop_protocol":55}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/runtime/codex/key" {
			fmt.Fprintf(w, `{"source":"cli","state":%q}`, state.Load())
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "https://")
	for _, origin := range []string{"https://100.80.1.2:7331", "https://100.80.1.3:7331"} {
		output := captureHostOutput(t, func() error { return hostAccess(t.Context(), dir, address, origin, false) })
		if strings.Contains(output, token) {
			t.Fatal("access instructions exposed the owner token")
		}
		profile, err := localconfig.ReadConnection(filepath.Join(dir, "owner.json"))
		if err != nil || profile.Server != origin || profile.Token != token {
			t.Fatalf("private access file was not refreshed: %v", err)
		}
	}
	for _, tc := range []struct{ state, want string }{
		{"sign_in_required", "buddy-server login"},
		{"missing", "executable is missing"},
		{"unknown", "timed out"},
		{"signed_in", "signed_in (cli)"},
	} {
		state.Store(tc.state)
		output := captureHostOutput(t, func() error { return hostStatus(t.Context(), dir, address) })
		if !strings.Contains(output, tc.want) || strings.Contains(output, token) {
			t.Fatalf("status %s did not give useful, private diagnostics: %q", state, output)
		}
	}
	if err := hostAccess(t.Context(), dir, address, "http://100.80.1.2:7331", false); err == nil {
		t.Fatal("access helper accepted plaintext HTTP")
	}
}

func captureHostOutput(t *testing.T, fn func() error) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = previous }()
	err = fn()
	_ = write.Close()
	data, readErr := io.ReadAll(read)
	_ = read.Close()
	if err != nil || readErr != nil {
		t.Fatalf("host helper: %v, %v", err, readErr)
	}
	return string(data)
}
