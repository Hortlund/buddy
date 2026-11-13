package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

type connectionsTab int

const (
	connectionsHome connectionsTab = iota
	connectionsRuntimes
	connectionsExecution
	connectionsIntegrations
)

func (a *desktop) connections(c *ui.Context) {
	tabs := [...]struct {
		key   string
		title string
		icon  *ui.SVG
		show  func(*ui.Context)
	}{
		connectionsHome: {"connections-home", "Home & devices", hostMark, func(c *ui.Context) {
			a.hosting(c)
			a.mobileDevices(c)
		}},
		connectionsRuntimes:  {"connections-runtimes", "Runtimes", runtimeMark, a.runtimes},
		connectionsExecution: {"connections-execution", "Execution", workMark, a.executionSettings},
		connectionsIntegrations: {"connections-integrations", "Integrations", linkMark, func(c *ui.Context) {
			a.integrationSettings(c)
			a.mcpSettings(c)
		}},
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(880).AlignSelf(ui.Center).Padding(30, 28, 16).Gap(18).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(13).Children(func() {
				ui.Box(c).Size(42, 42).Radius(13).Background(t.Accent.Alpha(.12)).Center().Children(func() {
					ui.Icon(c, linkMark).Size(20, 20).TextColor(t.Accent)
				})
				ui.Text(c, "Connections").Role(ui.RoleHeading).FontSize(29).FontWeight(710).LetterSpacing(-1)
			})
			ui.Row(c).Gap(4).Padding(4).Wrap().Background(t.Surface).Radius(12).Border(1, t.Border).Children(func() {
				for i, tab := range tabs {
					id := connectionsTab(i)
					selected := a.connectionTab == id
					btn := ui.ButtonBase(c.Key(tab.key)).Label(tab.title).Selected(selected).
						Height(32).Padding(0, 10).Gap(6).Radius(8).OnClick(func() { a.connectionTab = id })
					bg, color := t.Surface, t.TextMuted
					if selected || btn.Hovered() {
						bg, color = t.SurfacePressed, t.Text
					}
					btn.Background(btn.AnimateColor("fill", bg, ui.Animation{Duration: 140 * time.Millisecond})).Children(func() {
						ui.Icon(c, tab.icon).Size(14, 14).TextColor(color)
						ui.Text(c, tab.title).SingleLine().FontSize(12).FontWeight(620).TextColor(color)
					})
				}
			})
		})
		ui.Scroll(c.Key(a.connectionTab)).Grow(1).MinHeight(0).FillWidth().Children(func() {
			panel := ui.Column(c).FillWidth().MaxWidth(880).AlignSelf(ui.Center).Padding(0, 28, 28).Gap(13)
			revealPanel(c, panel)
			panel.Children(func() { tabs[a.connectionTab].show(c) })
		})
	})
}

func revealPanel(c *ui.Context, panel ui.Element) {
	if c.Preferences().ReduceMotion {
		return
	}
	entered := ui.Local(panel, "entered", func() bool { return false })
	if !*entered {
		*entered = true
		panel.AnimatePose(ui.Pose{Y: 6, Fade: .28}, ui.Snappy)
		c.After(time.Millisecond)
		return
	}
	panel.AnimatePose(ui.Pose{}, ui.Animation{Duration: 260 * time.Millisecond})
}
