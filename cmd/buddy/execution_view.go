package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type executionView struct {
	vms                                                 []tasks.LumeVM
	hosts                                               []tasks.Device
	projects                                            []tasks.Project
	config                                              tasks.ExecutionConfig
	loading, busy                                       bool
	next                                                time.Time
	err, loadErr, name, description, checkout, selected string
}

func (a *desktop) refreshExecution(c *ui.Context) {
	v := &a.execution
	if a.window == nil || !a.online || a.serverProtocol < api.ExecutionProtocol || v.loading {
		return
	}
	if wait := time.Until(v.next); wait > 0 {
		c.After(wait)
		return
	}
	v.loading = true
	v.next = time.Now().Add(15 * time.Second)
	client, win := a.client, a.window
	go func() {
		hosts, err := client.Hosts(a.ctx)
		var vms []tasks.LumeVM
		for _, host := range hosts {
			if host.ID == "server" && host.Profile != nil {
				for _, cap := range host.Profile.Capabilities {
					if cap.ID == "lume" && cap.Ready {
						vms, _ = client.LumeVMs(a.ctx)
					}
				}
			}
		}
		var projects []tasks.Project
		var cfg tasks.ExecutionConfig
		if err == nil {
			projects, err = client.Projects(a.ctx)
		}
		if err == nil {
			cfg, err = client.ExecutionConfig(a.ctx)
		}
		win.Update(func() {
			if a.client != client {
				return
			}
			v.finishRefresh(hosts, projects, cfg, vms, err)
		})
	}()
}

func (v *executionView) finishRefresh(hosts []tasks.Device, projects []tasks.Project, cfg tasks.ExecutionConfig, vms []tasks.LumeVM, err error) {
	v.loading = false
	if err != nil {
		v.loadErr = errorMessage(err, "Couldn't refresh hosts and projects.")
		return
	}
	v.hosts, v.projects, v.config, v.vms, v.loadErr = hosts, projects, cfg, vms, ""
}

func (a *desktop) executionSettings(c *ui.Context) {
	if a.serverProtocol < api.ExecutionProtocol {
		message := "Update the Buddy server to manage execution."
		if !a.online {
			message = "Connect to a Buddy server to manage execution."
		} else if a.protocolChecked.IsZero() {
			message = "Checking server compatibility…"
		}
		ui.Text(c, message).TextColor(c.Theme().TextMuted)
		return
	}
	a.refreshExecution(c)
	v := &a.execution
	sectionLabel(c, "Hosts and capabilities")
	ui.Column(c).FillWidth().Radius(12).Border(1, c.Theme().Border).Background(c.Theme().Surface).Children(func() {
		for _, host := range v.hosts {
			ui.Column(c.Key(host.ID)).FillWidth().Padding(13).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, host.Name).FontWeight(650).Grow(1)
					state := "Offline"
					if host.Online {
						state = "Online"
					}
					connectionStatus(c, state, host.Online)
				})
				if host.Profile == nil || host.Profile.OS == "" {
					ui.Text(c, "Update buddyworker to discover this host's tools and projects.").FontSize(12).TextColor(c.Theme().TextMuted)
					return
				}
				profile := host.Profile
				ui.Text(c, profile.OS+" · "+profile.Architecture+" · "+profile.Environment+" · "+strconv.Itoa(profile.CPUs)+" CPUs · "+strconv.Itoa(profile.Slots)+" slots").FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Row(c).Wrap().Gap(12).Children(func() {
					for _, cap := range profile.Capabilities {
						if !cap.Installed {
							continue
						}
						label := cap.ID
						if !cap.Enabled {
							label += " (disabled)"
						} else if !cap.Ready {
							label += " (needs setup)"
						}
						ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted)
					}
				})
				if len(profile.Projects) > 0 {
					ui.Text(c, "Project checkouts: "+projectBindingNames(v.projects, profile.Projects)).FontSize(12).TextColor(c.Theme().TextMuted)
				}
				if host.ID == "server" {
					ui.Row(c).Wrap().Gap(7).Children(func() {
						for _, provider := range []struct{ id, name string }{{"playwright", "Browser"}, {"cua", "Cua desktop"}} {
							enabled := false
							for _, setup := range v.config.Automation {
								if setup.Provider == provider.id {
									enabled = true
								}
							}
							label := "Enable " + provider.name
							if enabled {
								label = "Disable " + provider.name
							}
							ui.Button(c.Key(provider.id), label).Height(28).Disabled(v.busy).OnClick(func() {
								a.changeExecution(func(ctx context.Context, client *api.Client) error {
									return client.EnableAutomation(ctx, provider.id, !enabled)
								})
							})
						}
						ui.Button(c, "Setup instructions").Height(28).OnClick(func() { c.Services().OpenURL("https://github.com/Hortlund/buddy/blob/main/docs/execution.md") })
					})
				}
			})
		}
	})
	if len(v.vms) > 0 {
		sectionLabel(c, "Lume environments")
		ui.Text(c, "Lume provides the VM; Cua Driver runs inside it. Pair a Buddy worker in each guest to make its tools and projects available.").FontSize(12).TextColor(c.Theme().TextMuted)
		for _, vm := range v.vms {
			ui.Row(c.Key(vm.Name)).FillWidth().Padding(10).Gap(8).Children(func() {
				ui.Text(c, vm.Name).Grow(1)
				ui.Text(c, vm.Status).FontSize(12).TextColor(c.Theme().TextMuted)
				action, label := "start", "Start"
				if vm.Status == "running" {
					action, label = "stop", "Shut down"
				}
				ui.Button(c, label).Disabled(v.busy).OnClick(func() {
					a.changeExecution(func(ctx context.Context, client *api.Client) error { return client.ChangeLumeVM(ctx, vm.Name, action) })
				})
			})
		}
	}
	sectionLabel(c, "Projects")
	ui.Column(c).FillWidth().Gap(9).Children(func() {
		for _, project := range v.projects {
			ui.Row(c.Key(project.ID)).FillWidth().Padding(10).Gap(8).Radius(9).Border(1, c.Theme().Border).Children(func() {
				ui.Button(c, "Edit "+project.Name).Grow(1).Justify(ui.Start).OnClick(func() {
					v.selected, v.name, v.description = project.ID, project.Name, project.Description
					v.checkout = ""
					for _, binding := range v.config.Projects {
						if binding.ProjectID == project.ID {
							v.checkout = binding.Path
						}
					}
				})
				ui.Text(c, project.ID).FontSize(11).Selectable().TextColor(c.Theme().TextMuted)
				ui.Button(c, "Remove").Height(28).Disabled(v.busy).OnClick(func() { a.removeExecutionProject(project) })
			})
		}
		ui.Column(c).FillWidth().Padding(13).Gap(8).Radius(10).Border(1, c.Theme().Border).Background(c.Theme().Surface).Children(func() {
			ui.TextInput(c.Key("project-name"), &v.name).Label("Project name").Placeholder("Project name").FillWidth().Height(32)
			ui.TextInput(c.Key("project-purpose"), &v.description).Label("Project purpose").Placeholder("What belongs in this project?").FillWidth().Height(32)
			ui.TextInput(c.Key("project-checkout"), &v.checkout).Label("Checkout on the Buddy server").Placeholder("Server checkout path, optional").FillWidth().Height(32)
			ui.Row(c).Gap(8).Children(func() {
				label := "Create project"
				if v.selected != "" {
					label = "Save project"
				}
				ui.PrimaryButton(c, label).Height(29).Disabled(v.busy || strings.TrimSpace(v.name) == "").OnClick(a.saveExecutionProject)
				if v.selected != "" {
					ui.Button(c, "New project").Height(29).OnClick(func() { v.selected, v.name, v.description, v.checkout = "", "", "", "" })
				}
			})
		})
		if v.err != "" {
			ui.Text(c, v.err).FontSize(12).TextColor(c.Theme().Danger)
		}
		if v.loadErr != "" {
			ui.Text(c, v.loadErr).FontSize(12).TextColor(c.Theme().Danger)
		}
	})
}

func (a *desktop) removeExecutionProject(project tasks.Project) {
	if a.window == nil || a.execution.busy {
		return
	}
	win := a.window
	a.changeExecution(func(ctx context.Context, client *api.Client) error {
		choice, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "Remove project",
			Message: "Remove " + project.Name + " from Buddy?",
			Detail:  "Checkout files, worker branches, and assignment history are kept. Stop active assignments first.",
			Buttons: []string{"Cancel", "Remove project"}, DestructiveButtons: []int{1},
		})
		if err != nil || choice.Button != 1 {
			return err
		}
		if err := client.RemoveProject(ctx, project.ID); err != nil {
			return err
		}
		win.Update(func() {
			if a.execution.selected == project.ID {
				a.execution.selected, a.execution.name, a.execution.description, a.execution.checkout = "", "", "", ""
			}
		})
		return nil
	})
}

func projectBindingNames(projects []tasks.Project, bindings []tasks.ProjectBinding) string {
	var names []string
	for _, binding := range bindings {
		name := binding.ProjectID
		for _, p := range projects {
			if p.ID == binding.ProjectID {
				name = p.Name
			}
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func (a *desktop) changeExecution(action func(context.Context, *api.Client) error) {
	v := &a.execution
	if a.window == nil || a.client == nil || v.busy {
		return
	}
	v.busy, v.err = true, ""
	client, win := a.client, a.window
	go func() {
		err := action(a.ctx, client)
		win.Update(func() {
			if a.client != client {
				return
			}
			v.busy = false
			v.next = time.Time{}
			if err != nil {
				v.err = errorMessage(err, "Couldn't update execution settings.")
			}
		})
	}()
}

func (a *desktop) saveExecutionProject() {
	v := &a.execution
	project := tasks.Project{ID: v.selected, Name: v.name, Description: v.description}
	checkout := v.checkout
	cfg := v.config
	win := a.window
	a.changeExecution(func(ctx context.Context, client *api.Client) error {
		p, err := client.SaveProject(ctx, project)
		if err != nil {
			return err
		}
		win.Update(func() {
			if v.name == project.Name && v.description == project.Description && v.checkout == checkout {
				v.selected = p.ID
			}
		})
		bindings := []tasks.ProjectBinding{}
		for _, binding := range cfg.Projects {
			if binding.ProjectID != p.ID {
				bindings = append(bindings, binding)
			}
		}
		if checkout != "" {
			bindings = append(bindings, tasks.ProjectBinding{ProjectID: p.ID, Path: checkout})
		}
		cfg.Projects = bindings
		return client.SaveExecutionConfig(ctx, cfg)
	})
}
