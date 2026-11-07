package tailnet

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"buddy/internal/api"
	"buddy/internal/localconfig"
)

func TestTailnetPairingChecksPrivateTCPForwarding(t *testing.T) {
	const local = "https://127.0.0.1:7331"
	const private = `{"TCP":{"7331":{"TCPForward":"127.0.0.1:7331"}}}`
	for _, tc := range []struct {
		name, serve, etag, origin, problem string
		blocked, canEnable, enable         bool
		writeStatus                        int
	}{
		{name: "unconfigured", serve: "null", etag: "version-1", canEnable: true},
		{name: "private raw TCP", serve: private, origin: "https://100.100.100.4:7331"},
		{name: "shields up", serve: private, blocked: true, problem: "Allow incoming connections"},
		{name: "public funnel", serve: `{"TCP":{"7331":{"TCPForward":"127.0.0.1:7331"}},"AllowFunnel":{"mac.example.ts.net:7331":true}}`, problem: "Funnel"},
		{name: "port claimed before enabling", serve: `{"TCP":{"7331":{"TCPForward":"127.0.0.1:8000"}}}`, enable: true, problem: "another route"},
		{name: "TLS termination", serve: `{"TCP":{"7331":{"TCPForward":"127.0.0.1:7331","TerminateTLS":"mac.example.ts.net"}}}`, problem: "another route"},
		{name: "temporary serve", serve: `{"Foreground":{"session":` + private + `}}`, problem: "temporary"},
		{name: "no conditional updates", serve: "null", enable: true, problem: "protect existing routes"},
		{name: "first forwarding rule", serve: "null", etag: "version-1", enable: true, writeStatus: http.StatusOK, origin: "https://100.100.100.4:7331"},
		{name: "preserves other routes", serve: `{"TCP":{"8443":{"HTTPS":true}},"Web":{"example.ts.net:8443":{"Handlers":{}}},"Services":{"svc:demo":{"Tun":true}}}`, etag: "version-1", enable: true, writeStatus: http.StatusOK, origin: "https://100.100.100.4:7331"},
		{name: "configuration changed during write", serve: "null", etag: "version-1", enable: true, writeStatus: http.StatusPreconditionFailed, problem: "configuration changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			localAPI, _ := tailnetTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/localapi/v0/prefs":
					_ = json.NewEncoder(w).Encode(struct{ ShieldsUp bool }{tc.blocked})
				case r.Method == http.MethodGet && r.URL.Path == "/localapi/v0/serve-config":
					w.Header().Set("ETag", tc.etag)
					_, _ = w.Write([]byte(tc.serve))
				case r.Method == http.MethodPost && r.URL.Path == "/localapi/v0/serve-config" && tc.writeStatus != 0:
					writes.Add(1)
					if got := r.Header.Get("If-Match"); got != tc.etag {
						t.Errorf("If-Match = %q, want %q", got, tc.etag)
					}
					var config map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
						t.Errorf("decode write: %v", err)
					}
					var ports map[string]json.RawMessage
					if err := json.Unmarshal(config["TCP"], &ports); err != nil {
						t.Errorf("decode TCP ports: %v", err)
					}
					if got := string(ports["7331"]); got != `{"TCPForward":"127.0.0.1:7331"}` {
						t.Errorf("unexpected Buddy forward: %s", got)
					}
					if tc.name == "preserves other routes" && (string(ports["8443"]) != `{"HTTPS":true}` ||
						string(config["Web"]) != `{"example.ts.net:8443":{"Handlers":{}}}` ||
						string(config["Services"]) != `{"svc:demo":{"Tun":true}}`) {
						t.Errorf("write changed another Tailscale setting: %v", config)
					}
					w.WriteHeader(tc.writeStatus)
				default:
					t.Errorf("unexpected Tailscale request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			plan, err := localAPI.pairing(t.Context(), nil, local, netip.MustParseAddr("100.100.100.4"), tc.enable)
			if tc.problem != "" {
				if err == nil || !strings.Contains(err.Error(), tc.problem) || plan != (tailnetPairing{}) {
					t.Fatalf("unexpected plan for blocked setup: %+v, %v", plan, err)
				}
			} else if err != nil || plan.origin != tc.origin || plan.canEnable != tc.canEnable {
				t.Fatalf("unexpected pairing plan: %+v, %v", plan, err)
			}
			var wantWrites int32
			if tc.writeStatus != 0 {
				wantWrites = 1
			}
			if got := writes.Load(); got != wantWrites {
				t.Errorf("wrote config %d times, want %d", got, wantWrites)
			}
		})
	}
}

func TestTailnetPairingFallsBackToWorkingCLI(t *testing.T) {
	_, creds := tailnetTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/localapi/v0/status" || r.URL.Query().Get("peers") != "false" {
			t.Errorf("unexpected Tailscale status request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"BackendState":"Running","TailscaleIPs":["fd7a:115c:a1e0::4","100.100.100.4"]}`))
	})
	firstDir, appDir := t.TempDir(), t.TempDir()
	broken, working := filepath.Join(firstDir, "tailscale"), filepath.Join(appDir, "Tailscale")
	for binary, details := range map[string]string{
		broken:  "curl --unix-socket " + filepath.Join(firstDir, "unavailable.sock") + " http://local-tailscaled.sock/localapi/v0/status",
		working: creds,
	} {
		script := "#!/bin/sh\n" +
			"[ \"$TAILSCALE_BE_CLI\" = 1 ] && [ \"$*\" = 'debug local-creds' ] || exit 2\n" +
			"printf '%s\\n' " + shQuote(details) + "\n"
		if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", firstDir)
	local, addr, err := findRunningTailnet(t.Context(), []string{"tailscale", working})
	if err != nil {
		t.Fatal(err)
	}
	defer local.client.CloseIdleConnections()
	if addr.String() != "100.100.100.4" {
		t.Fatalf("unexpected Tailscale address %s", addr)
	}
}

func TestTailnetPairingPreservesIPv6Localhost(t *testing.T) {
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
	published, err := localconfig.ConnectionCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"service":"buddy","desktop_protocol":42}`))
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()

	ipv6 := listener.Addr().String()
	_, port, _ := net.SplitHostPort(ipv6)
	endpoint := "https://localhost:" + port
	target, gotPort, ok := localTailnetTarget(endpoint)
	if !ok || gotPort != port || target != "localhost:"+port {
		t.Fatalf("localhost target was rewritten: %s, %s, %v", target, gotPort, ok)
	}
	client := api.NewConnectionClient(localconfig.Connection{Server: endpoint, Token: token, Certificate: published})
	defer client.Close()
	if !matchesBuddyForward(t.Context(), client, target, port, ipv6) {
		t.Fatal("did not recognize Buddy's IPv6 loopback listener")
	}
	if matchesBuddyForward(t.Context(), client, target, port, "127.0.0.1:"+port) {
		t.Fatal("accepted a forward to a different loopback listener")
	}
}

func tailnetTestAPI(t *testing.T, handle http.HandlerFunc) (tailnetLocalAPI, string) {
	t.Helper()
	const password = "local-fixture-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != "" || token != password || r.Host != "local-tailscaled.sock" {
			t.Error("unexpected Tailscale LocalAPI authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handle(w, r)
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	creds := "curl -u:" + password + " http://localhost:" + port + "/localapi/v0/status"
	local, err := parseTailnetLocalAPI([]byte(creds))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(local.client.CloseIdleConnections)
	return local, creds
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
