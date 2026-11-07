package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"

	"github.com/egoist/mygo/ui"
)

const tailscaleSetupURL = "https://tailscale.com/download"
const tailnetPairingTimeout = 12 * time.Second

var errTailscaleUnversioned = errors.New("This Tailscale version can’t protect existing routes during setup. Update Tailscale and check again.")

type tailnetPairing struct {
	origin                  string
	canEnable, needsInstall bool
}

type tailnetServeConfig struct {
	TCP map[string]struct {
		TCPForward    string
		TerminateTLS  string
		ProxyProtocol int
		HTTP, HTTPS   bool
	}
	AllowFunnel map[string]bool
	Foreground  map[string]tailnetServeConfig
}

func localTailnetTarget(endpoint string) (string, string, bool) {
	if localconfig.ValidateServerURL(endpoint) != nil {
		return "", "", false
	}
	u, _ := url.Parse(endpoint)
	if !loopbackHost(u.Hostname()) {
		return "", "", false
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return "", "", false
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	} else {
		host = strings.ToLower(host)
	}
	value := strconv.FormatUint(port, 10)
	return net.JoinHostPort(host, value), value, true
}

func inspectTailnetPairing(ctx context.Context, client *api.Client, endpoint string, enable bool) (tailnetPairing, error) {
	local, address, err := findRunningTailnet(ctx, []string{
		"tailscale",
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"/opt/homebrew/bin/tailscale",
		"/usr/local/bin/tailscale",
	})
	if err != nil {
		return tailnetPairing{needsInstall: errors.Is(err, errTailscaleMissing)}, err
	}
	defer local.client.CloseIdleConnections()
	return local.pairing(ctx, client, endpoint, address, enable)
}

func (local tailnetLocalAPI) pairing(ctx context.Context, client *api.Client, endpoint string, address netip.Addr, enable bool) (tailnetPairing, error) {
	target, port, ok := localTailnetTarget(endpoint)
	if !ok {
		return tailnetPairing{}, errors.New("Use the hosted Buddy server’s address for this home.")
	}
	var prefs struct{ ShieldsUp *bool }
	if _, err := local.read(ctx, "prefs", &prefs); err != nil || prefs.ShieldsUp == nil {
		return tailnetPairing{}, errors.New("Couldn’t check whether Tailscale allows incoming connections. Check your Tailscale version and try again.")
	}
	if *prefs.ShieldsUp {
		return tailnetPairing{}, errors.New("Turn on Allow incoming connections in Tailscale Settings, then check again.")
	}

	var document json.RawMessage
	etag, err := local.read(ctx, "serve-config", &document)
	var config tailnetServeConfig
	if err != nil || json.Unmarshal(document, &config) != nil {
		return tailnetPairing{}, errors.New("Couldn’t check Tailscale Serve. Check your Tailscale version and try again.")
	}
	ready, err := config.buddyForward(port, func(forward string) bool {
		return matchesBuddyForward(ctx, client, target, port, forward)
	})
	if err != nil {
		return tailnetPairing{}, err
	}
	if ready {
		return tailnetPairing{origin: "https://" + net.JoinHostPort(address.String(), port)}, nil
	}
	if etag == "" {
		return tailnetPairing{}, errTailscaleUnversioned
	}
	if !enable {
		return tailnetPairing{canEnable: true}, nil
	}
	if err := local.enableBuddyForward(ctx, document, etag, port, target); err != nil {
		return tailnetPairing{}, err
	}
	return tailnetPairing{origin: "https://" + net.JoinHostPort(address.String(), port)}, nil
}

func matchesBuddyForward(ctx context.Context, client *api.Client, target, port, forward string) bool {
	alias, aliasPort, ok := localTailnetTarget("https://" + forward)
	if !ok || aliasPort != port {
		return false
	}
	if alias == target {
		return true
	}
	// IPv4, IPv6 and localhost can have different listeners. Check the alias
	// against this home's certificate before treating it as the same endpoint.
	if client == nil {
		return false
	}
	profile, err := client.OwnerProfile("https://" + alias)
	if err != nil {
		return false
	}
	aliasClient := api.NewConnectionClient(profile)
	defer aliasClient.Close()
	_, err = aliasClient.DesktopProtocol(ctx)
	return err == nil
}

func (local tailnetLocalAPI) enableBuddyForward(ctx context.Context, document json.RawMessage, etag, port, target string) error {
	if etag == "" {
		return errTailscaleUnversioned
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(document, &config); err != nil {
		return err
	}
	if config == nil {
		config = make(map[string]json.RawMessage)
	}
	var ports map[string]json.RawMessage
	if raw := config["TCP"]; raw != nil {
		if err := json.Unmarshal(raw, &ports); err != nil {
			return err
		}
	}
	if ports == nil {
		ports = make(map[string]json.RawMessage)
	}
	ports[port], _ = json.Marshal(struct{ TCPForward string }{target})
	config["TCP"], _ = json.Marshal(ports)
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	// If-Match binds the write to the exact config that was just checked.
	resp, err := local.request(ctx, http.MethodPost, "serve-config", etag, body)
	if err != nil {
		return errors.New("Couldn’t save Tailscale forwarding. Check Tailscale and try again.")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusPreconditionFailed:
		return errors.New("Tailscale’s forwarding configuration changed. Check Tailscale before trying again.")
	case http.StatusForbidden, http.StatusUnauthorized:
		return errors.New("Tailscale denied the change. A Tailscale operator can enable forwarding on this Mac.")
	default:
		return errors.New("Tailscale couldn’t enable forwarding. Check its settings and try again.")
	}
}

func (config tailnetServeConfig) buddyForward(port string, matchesTarget func(string) bool) (bool, error) {
	for address, enabled := range config.AllowFunnel {
		_, funnelPort, err := net.SplitHostPort(address)
		if enabled && err == nil && funnelPort == port {
			return false, fmt.Errorf("Tailscale Funnel is enabled on port %s. Turn off that public route before pairing through private Serve.", port)
		}
	}
	for _, session := range config.Foreground {
		ready, err := session.buddyForward(port, matchesTarget)
		if err != nil {
			return false, err
		}
		if ready {
			return false, fmt.Errorf("A temporary Tailscale Serve session is using port %s. Stop that session to set up background sharing.", port)
		}
	}
	handler, exists := config.TCP[port]
	if !exists {
		return false, nil
	}
	if handler.TerminateTLS == "" && handler.ProxyProtocol == 0 && !handler.HTTP && !handler.HTTPS && matchesTarget(handler.TCPForward) {
		return true, nil
	}
	return false, fmt.Errorf("Tailscale Serve already uses port %s for another route. Review that mapping before setting up Buddy.", port)
}

func (a *desktop) checkPhoneTailnet() { a.updatePhoneTailnet(false) }

func (a *desktop) updatePhoneTailnet(enable bool) {
	v := &a.mobile
	if v.busy || a.window == nil || (enable && (!a.online || !v.tailnet.canEnable)) {
		return
	}
	v.busy, v.problem, v.tailnet = true, "", tailnetPairing{}
	v.invitation, v.qr = "", nil
	win, client, endpoint := a.window, a.client, a.endpoint
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, tailnetPairingTimeout)
		defer cancel()
		plan, err := inspectTailnetPairing(ctx, client, endpoint, enable)
		win.Update(func() {
			v.busy, v.tailnet = false, plan
			if err != nil {
				v.problem = err.Error()
				return
			}
		})
	}()
}

func (a *desktop) phoneTailnetForm(c *ui.Context) {
	v := &a.mobile
	ui.Text(c, "Connect this Mac and your iPhone to the same Tailscale network. Buddy stays reachable while this Mac is awake.").
		FontSize(12).TextColor(c.Theme().TextMuted)
	if v.tailnet.canEnable {
		ui.Text(c, "Allow devices on your Tailscale network to reach Buddy on this Mac. Pair each iPhone with its own code.").
			FontSize(12).TextColor(c.Theme().TextMuted)
	}
	if v.tailnet.origin != "" {
		ui.Text(c, "Private TCP forwarding configured · "+v.tailnet.origin).FontSize(12).Selectable()
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Button(c.Key("check-tailnet"), "Check Tailscale").Disabled(v.busy || !a.online).OnClick(a.checkPhoneTailnet)
		switch {
		case v.tailnet.canEnable:
			ui.PrimaryButton(c.Key("enable-tailnet"), "Enable private forwarding").Disabled(v.busy || !a.online).OnClick(func() {
				a.updatePhoneTailnet(true)
			})
		case v.tailnet.origin != "":
			ui.PrimaryButton(c.Key("tailnet-qr"), "Generate QR code").Disabled(v.busy || !a.online).OnClick(a.generateMobilePairing)
		case v.tailnet.needsInstall:
			ui.Button(c.Key("tailscale-setup"), "Get Tailscale").OnClick(func() { c.OpenURL(tailscaleSetupURL) })
		}
	})
}
