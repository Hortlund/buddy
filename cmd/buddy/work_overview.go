package main

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func workerRole(item tasks.WorkItem) string {
	role := item.Role
	if role == "" {
		role = "general"
	}
	return strings.ToUpper(role[:1]) + role[1:] + " worker"
}

func workMatches(work *workBrowser, item tasks.WorkItem) bool {
	return (work.budFilter == "" || work.budFilter == item.AgentID) && (work.projectFilter == "" || work.projectFilter == item.ProjectID) && (work.deviceFilter == "" || work.deviceFilter == workerLocation(item))
}

func workFilters(c *ui.Context, work *workBrowser) {
	ui.Row(c).Wrap().Gap(5).Children(func() {
		for _, filter := range []struct {
			name  string
			value *string
			key   func(tasks.WorkItem) (string, string)
		}{
			{"Buds", &work.budFilter, func(item tasks.WorkItem) (string, string) { return item.AgentID, item.AgentName }},
			{"Projects", &work.projectFilter, func(item tasks.WorkItem) (string, string) { return item.ProjectID, item.ProjectName }},
			{"Hosts", &work.deviceFilter, func(item tasks.WorkItem) (string, string) { return workerLocation(item), workerLocation(item) }},
		} {
			choices := map[string]string{}
			for _, item := range work.visibleItems() {
				id, name := filter.key(item)
				if id != "" {
					choices[id] = name
				}
			}
			label := filter.name
			if *filter.value != "" {
				label = choices[*filter.value]
			}
			ui.MenuButton(c.Key(filter.name), label, func(menu *ui.Menu) {
				if menu.Item("All " + strings.ToLower(filter.name)).Chosen() {
					*filter.value = ""
				}
				var ids []string
				for id := range choices {
					ids = append(ids, id)
				}
				slices.Sort(ids)
				for _, id := range ids {
					if menu.Item(choices[id]).Chosen() {
						*filter.value = id
					}
				}
			}).Height(27)
		}
	})
}

func (a *desktop) workOverview(c *ui.Context, work *workBrowser) {
	ui.Text(c, "Your team at work").Role(ui.RoleHeading).FontSize(21).FontWeight(680)
	ui.Text(c, "Open an assignment to follow its workers, review deliverables, or ask the owning Bud a question.").FontSize(13).TextColor(c.Theme().TextMuted).Margin(8, 0, 18)
	items := work.visibleItems()
	counts := map[string]int{}
	for _, item := range items {
		if !workMatches(work, item) {
			continue
		}
		switch {
		case item.Status == tasks.NeedsInput:
			counts["Needs you"]++
		case item.Status.Active():
			counts["Active"]++
		default:
			counts["Recent"]++
		}
	}
	ui.Row(c).Wrap().Gap(10).Children(func() {
		for _, label := range []string{"Needs you", "Active", "Recent"} {
			ui.Column(c).Width(150).Padding(14).Radius(10).Border(1, c.Theme().Border).Gap(5).Children(func() {
				ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Text(c, strconv.Itoa(counts[label])).FontSize(25).FontWeight(650)
			})
		}
	})
	sectionLabel(c, "Working Buds")
	for _, bud := range a.directory.Agents {
		var owned []tasks.WorkItem
		for _, item := range items {
			if item.AgentID == bud.ID && workMatches(work, item) && (item.Status.Active() || item.Status == tasks.NeedsInput) {
				owned = append(owned, item)
			}
		}
		if len(owned) == 0 {
			continue
		}
		ui.Column(c.Key(bud.ID)).FillWidth().Padding(12).Gap(8).Margin(8, 0, 0).Radius(10).Border(1, c.Theme().Border).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				a.drawProfile(c, a.directoryProfile(bud), 28, false, orbIdle)
				ui.Text(c, bud.Name).FontWeight(650)
			})
			for _, item := range owned {
				workRow(c.Key(item.ID), item, false, true, func() { work.selectItem(item) })
			}
		})
	}
}

func (a *desktop) executionWorkDetail(c *ui.Context, detail tasks.WorkDetails) {
	if detail.ProjectName != "" {
		ui.Text(c, "Project · "+detail.ProjectName).FontSize(12).FontWeight(620)
	}
	if len(detail.Requirements) > 0 {
		ui.Text(c, "Requires · "+strings.Join(detail.Requirements, ", ")).FontSize(11).TextColor(c.Theme().TextMuted)
	}
	for _, dependency := range detail.Dependencies {
		ui.Button(c.Key("depends-"+dependency.ID), "Depends on · "+dependency.AgentName+" · "+dependency.Title+" · "+workStatus(dependency.Status)).FillWidth().Justify(ui.Start).
			OnClick(func() { a.openWork(tasks.WorkItem{ID: dependency.ID, ConversationID: dependency.ConversationID}) })
	}
	if len(detail.Delegated) > 0 {
		sectionLabel(c, "Delegated work")
		for _, child := range detail.Delegated {
			ui.Button(c.Key("child-"+child.ID), child.AgentName+" · "+child.Title+" · "+workStatus(child.Status)).FillWidth().Justify(ui.Start).
				OnClick(func() { a.openWork(tasks.WorkItem{ID: child.ID, ConversationID: child.ConversationID}) })
		}
	}
	if len(detail.Sessions) > 0 {
		sectionLabel(c, "Worker sessions")
		for _, session := range detail.Sessions {
			ui.Column(c.Key(session.ID)).FillWidth().Padding(10).Gap(4).Radius(8).Background(c.Theme().SurfaceHover).Children(func() {
				host := session.Host
				if host == "server" {
					host = "Buddy server"
				}
				ui.Text(c, workerRole(detail.WorkItem)+" · "+host+" · "+workStatus(session.Status)).FontSize(12).FontWeight(620)
				if session.Branch != "" {
					ui.Text(c, session.Branch).FontSize(11).Selectable().TextColor(c.Theme().TextMuted)
				}
				ui.Text(c, session.Workspace).FontSize(11).Selectable().TextColor(c.Theme().TextMuted)
			})
		}
	}
	if len(detail.Artifacts) > 0 {
		sectionLabel(c, "Deliverables")
		for _, artifact := range detail.Artifacts {
			ui.Button(c.Key(artifact.ID), artifact.Name).Justify(ui.Start).OnClick(func() { a.openWorkArtifact(detail.ID, artifact.ID) })
		}
	}
}

func (a *desktop) automationWorkView(c *ui.Context, work *workBrowser, item tasks.WorkItem) {
	if !slices.Contains(item.Requirements, "browser") && !slices.Contains(item.Requirements, "desktop") {
		return
	}
	sectionLabel(c, "Automation view")
	if work.frameImage != nil {
		ui.Image(c, work.frameImage).Fit(ui.Contain).FillWidth().Height(250).Label("Most recent automation screenshot")
		ui.Text(c, "Captured "+work.frame.At.Local().Format("15:04:05")+" · "+work.frame.Provider).FontSize(11).TextColor(c.Theme().TextMuted)
	} else {
		ui.Text(c, "Screenshots appear as the worker observes its browser or desktop.").FontSize(12).TextColor(c.Theme().TextMuted)
	}
	if item.Status == tasks.Running {
		label := "Take control"
		if work.frame.Paused {
			label = "Resume automation"
		}
		ui.Button(c, label).OnClick(func() {
			client, win, job, paused := a.client, a.window, item.ID, !work.frame.Paused
			if client == nil || win == nil {
				return
			}
			go func() {
				err := client.PauseAutomation(a.ctx, job, paused)
				win.Update(func() {
					if work.selected.ID != job {
						return
					}
					if err != nil {
						work.stopErr = errorMessage(err, "Couldn't change automation control.")
					} else {
						work.frame.Paused = paused
						work.nextRead = time.Time{}
					}
				})
			}()
		})
		if work.frame.Paused {
			ui.Text(c, "Automation input is paused. Use the desktop or browser viewer, then resume here.").FontSize(12).TextColor(c.Theme().Warning)
		}
	}
}
