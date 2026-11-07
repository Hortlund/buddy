package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/localconfig"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	qrcode "github.com/skip2/go-qrcode"
)

type mobileView struct {
	clients                     []tasks.MobileClient
	origin, invitation, problem string
	loadProblem                 string
	expires, next               time.Time
	open, busy, loading         bool
	useTailnet                  bool
	tailnet                     tailnetPairing
	qr                          *ui.Bitmap
}

func (a *desktop) refreshMobileClients(c *ui.Context) {
	v := &a.mobile
	if a.window == nil || !a.online || a.serverProtocol < api.MobileProtocol || v.busy || v.loading {
		return
	}
	if delay := time.Until(v.next); delay > 0 {
		c.After(delay)
		return
	}
	v.loading, v.next = true, time.Now().Add(5*time.Second)
	win, client := a.window, a.client
	go func() {
		clients, err := client.MobileClients(a.ctx)
		win.Update(func() {
			v.loading = false
			if err != nil {
				v.loadProblem = errorMessage(err, "Couldn't load paired iPhones.")
				return
			}
			v.clients, v.loadProblem = clients, ""
		})
	}()
}

func mobileOrigin(origin string) bool {
	if localconfig.ValidateServerURL(origin) != nil {
		return false
	}
	u, _ := url.Parse(origin)
	ip := net.ParseIP(u.Hostname())
	return !loopbackHost(u.Hostname()) && (ip == nil || !ip.IsUnspecified())
}

func (a *desktop) mobileDevices(c *ui.Context) {
	v := &a.mobile
	a.refreshMobileClients(c)
	ready := a.online && a.serverProtocol >= api.MobileProtocol
	sectionLabel(c, "iPhone")
	connectionSection(c, func() {
		ui.Row(c).Padding(11, 13).Gap(9).AlignItems(ui.Center).Children(func() {
			connectionDetail(c, deviceMark, "Buddy for iPhone", "Use your Buds, inbox, and background work from your phone.")
			label := "Pair iPhone"
			if v.open {
				label = "Close pairing"
			}
			ui.Button(c.Key("iphone-pair"), label).Disabled(!ready || v.busy).OnClick(func() {
				v.open = !v.open
				v.invitation, v.qr, v.problem = "", nil, ""
				if v.open {
					_, _, v.useTailnet = localTailnetTarget(a.endpoint)
					if v.useTailnet {
						a.checkPhoneTailnet()
					} else if v.origin == "" && mobileOrigin(a.endpoint) {
						v.origin = a.endpoint
					}
				}
			})
		})
		if a.online && a.serverProtocol < api.MobileProtocol {
			mcpNote(c, "Update the Buddy server to pair an iPhone.")
		}
		if v.open {
			a.mobilePairingForm(c.Key("iphone-pairing"))
		}
		problem := v.problem
		if problem == "" {
			problem = v.loadProblem
		}
		if problem != "" {
			ui.Text(c, problem).Padding(8, 13).TextColor(c.Theme().Danger).FontSize(12)
		}
		for _, phone := range v.clients {
			ui.Row(c.Key(phone.ID)).Padding(8, 13).Gap(8).AlignItems(ui.Center).Children(func() {
				detail := "Paired " + phone.PairedAt.Local().Format("Jan 2")
				if phone.Notifications {
					detail += " · Notifications on"
				}
				connectionDetail(c, deviceMark, phone.Name, detail)
				ui.Button(c, "Remove").Label("Remove " + phone.Name).Disabled(!ready || v.busy).OnClick(func() { a.removeMobileClient(phone) })
			})
		}
	})
}

func (a *desktop) mobilePairingForm(c *ui.Context) {
	v := &a.mobile
	ui.Column(c).Padding(12, 13).Gap(8).Children(func() {
		if _, _, local := localTailnetTarget(a.endpoint); local {
			ui.Row(c).Gap(8).Children(func() {
				for _, mode := range []struct {
					label   string
					tailnet bool
				}{{"Tailscale", true}, {"Enter address", false}} {
					button := ui.Button
					if mode.tailnet == v.useTailnet {
						button = ui.PrimaryButton
					}
					button(c.Key("iphone-mode-"+mode.label), mode.label).Disabled(v.busy).OnClick(func() {
						if mode.tailnet != v.useTailnet {
							v.useTailnet, v.problem = mode.tailnet, ""
							v.invitation, v.qr = "", nil
							if mode.tailnet {
								a.checkPhoneTailnet()
							}
						}
					})
				}
			})
		}
		if v.useTailnet {
			a.phoneTailnetForm(c)
		} else {
			ui.Text(c, "Enter the HTTPS address your iPhone uses to reach this Buddy home.").
				FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				ui.TextInput(c.Key("iphone-origin"), &v.origin).Label("Buddy server address").
					Placeholder("https://buddy.example.net:7331").Grow(1).Disabled(v.busy)
				ui.PrimaryButton(c.Key("iphone-code"), "Generate QR code").
					Disabled(v.busy || !a.online || !mobileOrigin(strings.TrimSpace(v.origin))).OnClick(a.generateMobilePairing)
			})
		}
		if v.qr != nil {
			if time.Until(v.expires) > 0 {
				c.After(time.Until(v.expires))
				ui.Row(c).Gap(15).AlignItems(ui.Center).Children(func() {
					ui.Image(c.Key("iphone-qr"), v.qr).Width(184).Height(184).Label("One-time iPhone pairing QR code")
					ui.Column(c).Gap(6).Children(func() {
						ui.Text(c, "Scan this code in Buddy on your iPhone.").FontWeight(650)
						ui.Text(c, "The code expires at "+v.expires.Local().Format("15:04")+" and works once.").FontSize(12).TextColor(c.Theme().TextMuted)
						ui.Button(c.Key("iphone-copy"), "Copy pairing link").OnClick(func() { mygo.Clipboard.WriteText(v.invitation) })
					})
				})
			} else {
				ui.Text(c, "This code expired. Generate another to pair.").FontSize(12).TextColor(c.Theme().TextMuted)
			}
		}
	})
}

func (a *desktop) generateMobilePairing() {
	v := &a.mobile
	if v.busy || !a.online || a.window == nil {
		return
	}
	v.busy, v.problem = true, ""
	v.invitation, v.qr = "", nil
	win, client, address := a.window, a.client, strings.TrimSpace(v.origin)
	useTailnet, endpoint := v.useTailnet, a.endpoint
	go func() {
		var plan tailnetPairing
		var err error
		fallback := "Couldn't generate the iPhone pairing code."
		if useTailnet {
			ctx, cancel := context.WithTimeout(a.ctx, tailnetPairingTimeout)
			plan, err = inspectTailnetPairing(ctx, client, endpoint, false)
			cancel()
			address = plan.origin
			if err == nil && address == "" {
				err = errors.New("Set up private TCP forwarding before generating a pairing code.")
			}
			if err != nil {
				fallback = err.Error()
			}
		}
		var link string
		var expires time.Time
		if err == nil {
			link, expires, err = client.CreateMobilePairing(a.ctx, address)
		}
		var data []byte
		if err == nil {
			data, err = qrcode.Encode(link, qrcode.Medium, 368)
		}
		win.Update(func() {
			v.busy = false
			if useTailnet {
				v.tailnet = plan
			}
			if err == nil {
				v.qr, err = ui.DecodeBitmap(data)
			}
			if err != nil {
				v.problem = errorMessage(err, fallback)
				return
			}
			v.invitation, v.expires = link, expires
		})
	}()
}

func (a *desktop) removeMobileClient(phone tasks.MobileClient) {
	v := &a.mobile
	if v.busy || !a.online || a.window == nil {
		return
	}
	v.busy, v.problem = true, ""
	win, client := a.window, a.client
	go func() {
		choice, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "Remove paired iPhone", Message: "Remove " + phone.Name + "?",
			Detail: "This iPhone will lose access to your Buddy server.", Buttons: []string{"Cancel", "Remove"}, DestructiveButtons: []int{1},
		})
		if err == nil && choice.Button == 1 {
			err = client.RevokeMobileClient(a.ctx, phone.ID)
		}
		win.Update(func() {
			v.busy, v.next = false, time.Time{}
			if err != nil {
				v.problem = errorMessage(err, "Couldn't remove the iPhone.")
			}
		})
	}()
}
