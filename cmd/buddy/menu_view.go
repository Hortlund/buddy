package main

import (
	"fmt"
	"strconv"

	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

var menuMark = svg(`<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>`)

func (a *desktop) menuButton(c *ui.Context) {
	unread := a.directory.AutonomyUnread
	label := "Menu"
	if unread > 0 {
		label = fmt.Sprintf("Menu · %d unread", unread)
	}
	btn := ui.ButtonBase(c.Key("nav-menu")).Label(label).FillWidth().Height(35).
		Gap(10).Padding(0, 9).Radius(8).Justify(ui.Start).Cursor(ui.CursorPointer).
		OnClick(func() { a.section = "menu" })
	if a.section == "menu" {
		btn.Background(c.Theme().SurfacePressed)
	} else if btn.Hovered() {
		btn.Background(c.Theme().Surface)
	}
	btn.Children(func() {
		ui.Icon(c, menuMark).Size(17, 17)
		ui.Text(c, "Menu").FontWeight(600).Grow(1)
		menuUnread(c, unread)
	})
}

func (a *desktop) menuPage(c *ui.Context) {
	controlPage(c.Key("menu-page"), "Menu", "Review activity and manage your Buddy workspace.", nil, func() {
		sectionLabel(c, "Activity")
		connectionSection(c, func() {
			menuDestination(c.Key("nav-work"), workMark, "Work", "Follow jobs and review results", 0,
				func() { a.openWork(tasks.WorkItem{}) })
			menuDestination(c.Key("nav-autonomy"), clockMark, "Autonomy", "Inbox, watches, and memory", a.directory.AutonomyUnread, a.showAutonomy)
			menuDestination(c.Key("nav-history"), historyMark, "Archived chats", "Open or restore archived groups", 0, func() { a.openHistory("") })
		})
		sectionLabel(c, "Settings and tools")
		connectionSection(c, func() {
			menuDestination(c.Key("nav-preferences"), settingsMark, "Settings", "Appearance, languages, and app info", 0, a.showPreferences)
			menuDestination(c.Key("nav-agent"), agentMark, "Bud settings", "Manage your Buds and their workspaces", 0, a.showBudSettings)
			menuDestination(c.Key("nav-connections"), linkMark, "Connections", "Servers, devices, and integrations", 0, a.showConnections)
			menuDestination(c.Key("nav-skills"), skillMark, "Skills", "Browse and manage reusable instructions", 0,
				func() { a.section = "skills" })
		})
	})
}

func menuDestination(c *ui.Context, icon *ui.SVG, title, detail string, unread int, action func()) {
	btn := ui.ButtonBase(c).Label(title).Description(detail).FillWidth().MinWidth(0).MinHeight(52).
		Gap(10).Padding(9, 12).Radius(8).Justify(ui.Start).OnClick(action)
	if btn.Hovered() {
		btn.Background(c.Theme().SurfaceHover)
	}
	btn.Children(func() {
		connectionDetail(c, icon, title, detail)
		menuUnread(c, unread)
		ui.Icon(c, arrowMark).Size(13, 13).Shrink(0).TextColor(c.Theme().TextMuted)
	})
}

func menuUnread(c *ui.Context, unread int) {
	if unread == 0 {
		return
	}
	ui.Box(c).Height(17).MinWidth(17).Padding(0, 4).Radius(8).Center().Shrink(0).
		Background(c.Theme().SurfacePressed).Children(func() {
		ui.Text(c, strconv.Itoa(unread)).FontSize(10).FontWeight(650)
	})
}
