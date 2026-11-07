package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type tailnetLocalAPI struct {
	client *http.Client
	token  string
}

var errTailscaleMissing = errors.New("Install and connect Tailscale on this machine and your other devices, then check again.")

func findRunningTailnet(ctx context.Context, candidates []string) (tailnetLocalAPI, netip.Addr, error) {
	found := false
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		binary, err := exec.LookPath(candidate)
		if err != nil || seen[binary] {
			continue
		}
		found, seen[binary] = true, true
		attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
		local, address, err := runningTailnet(attempt, binary)
		cancel()
		if err == nil {
			return local, address, nil
		}
	}
	if !found {
		return tailnetLocalAPI{}, netip.Addr{}, errTailscaleMissing
	}
	return tailnetLocalAPI{}, netip.Addr{}, errors.New("Couldn’t reach a connected Tailscale client. Open Tailscale on this machine and check again.")
}

func runningTailnet(ctx context.Context, binary string) (tailnetLocalAPI, netip.Addr, error) {
	cmd := exec.CommandContext(ctx, binary, "debug", "local-creds")
	cmd.Env = append(cmd.Environ(), "TAILSCALE_BE_CLI=1")
	cmd.WaitDelay = 250 * time.Millisecond
	data, err := cmd.Output()
	if err != nil {
		return tailnetLocalAPI{}, netip.Addr{}, err
	}
	local, err := parseTailnetLocalAPI(data)
	if err != nil {
		return tailnetLocalAPI{}, netip.Addr{}, err
	}
	var status struct {
		BackendState string
		TailscaleIPs []netip.Addr
	}
	_, err = local.read(ctx, "status?peers=false", &status)
	if err == nil && status.BackendState == "Running" {
		var address netip.Addr
		for _, ip := range status.TailscaleIPs {
			if ip.IsGlobalUnicast() && (!address.IsValid() || ip.Is4()) {
				address = ip
			}
			if address.Is4() {
				break
			}
		}
		if address.IsValid() {
			return local, address, nil
		}
	}
	local.client.CloseIdleConnections()
	return tailnetLocalAPI{}, netip.Addr{}, errors.New("Tailscale is disconnected or has no address")
}

// Parse the CLI's local connection instructions as data. They can contain a
// daemon password, so keep the output in memory and never run or log it.
func parseTailnetLocalAPI(data []byte) (tailnetLocalAPI, error) {
	fields := strings.Fields(string(data))
	network, address, token := "", "", ""
	switch {
	case len(fields) == 4 && fields[0] == "curl" && fields[1] == "--unix-socket" && filepath.IsAbs(fields[2]) &&
		fields[3] == "http://local-tailscaled.sock/localapi/v0/status":
		network, address = "unix", fields[2]
	case len(fields) == 3 && fields[0] == "curl" && strings.HasPrefix(fields[1], "-u:"):
		u, err := url.Parse(fields[2])
		token = strings.TrimPrefix(fields[1], "-u:")
		if err == nil && token != "" && u.Scheme == "http" && u.Hostname() == "localhost" &&
			u.Path == "/localapi/v0/status" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
			if port, err := strconv.ParseUint(u.Port(), 10, 16); err == nil && port != 0 {
				network, address = "tcp", net.JoinHostPort("127.0.0.1", strconv.FormatUint(port, 10))
			}
		}
	}
	if address == "" {
		return tailnetLocalAPI{}, errors.New("invalid Tailscale local connection details")
	}
	var dialer net.Dialer
	return tailnetLocalAPI{token: token, client: &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (local tailnetLocalAPI) request(ctx context.Context, method, path, etag string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock/localapi/v0/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if local.token != "" {
		req.SetBasicAuth("", local.token)
	}
	if etag != "" {
		req.Header.Set("If-Match", etag)
		req.Header.Set("Content-Type", "application/json")
	}
	return local.client.Do(req)
}

func (local tailnetLocalAPI) read(ctx context.Context, path string, value any) (string, error) {
	resp, err := local.request(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("Tailscale LocalAPI rejected the request")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return resp.Header.Get("ETag"), json.Unmarshal(data, value)
}
