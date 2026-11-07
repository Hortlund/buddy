package main

import (
	"context"
	"time"

	"buddy/internal/api"
	"buddy/internal/tailnet"
	"github.com/egoist/mygo/ui"
)

const tailscaleSetupURL = "https://tailscale.com/download"
const tailnetPairingTimeout = 12 * time.Second

type tailnetPairing struct {
	origin                  string
	canEnable, needsInstall bool
}

func inspectTailnetPairing(ctx context.Context, client *api.Client, endpoint string, enable bool) (tailnetPairing, error) {
	p, err := tailnet.Configure(ctx, client, endpoint, enable)
	return tailnetPairing{origin: p.Origin, canEnable: p.CanEnable, needsInstall: p.NeedsInstall}, err
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

func localTailnetTarget(endpoint string) (string, string, bool) { return tailnet.LocalTarget(endpoint) }
