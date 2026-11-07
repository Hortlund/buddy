package main

import (
	"slices"
	"strings"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

type watchScopes struct {
	connection string
	options    []api.ScopeOption
	loading    bool
	err        string
}

func (a *desktop) integration(id string) tasks.ProviderConnection {
	for _, connection := range a.integrations.catalog.Connections {
		if connection.ID == id {
			return connection
		}
	}
	return tasks.ProviderConnection{}
}

func (a *desktop) availableWatchConnections() []tasks.ProviderConnection {
	catalog := a.integrations.catalog
	var choices []tasks.ProviderConnection
	for _, connection := range catalog.Connections {
		if connection.Connected {
			choices = append(choices, connection)
		}
	}
	if catalog.LegacyGitHub || a.serverProtocol < api.IntegrationsProtocol {
		choices = append(choices, tasks.ProviderConnection{Source: tasks.GitHubCI, Name: "Server configured"})
	}
	if catalog.LegacyGmail || a.serverProtocol < api.IntegrationsProtocol {
		choices = append(choices, tasks.ProviderConnection{Source: tasks.GmailLabel, Name: "Server configured"})
	}
	return choices
}

func (a *desktop) startIntegrationWatch(connection tasks.ProviderConnection) {
	a.showAutonomy()
	a.autonomy.tab = "Watches"
	a.newConnectionWatch(connection)
}

func (a *desktop) newConnectionWatch(connection tasks.ProviderConnection) {
	v := &a.autonomy
	chat := a.currentConversation()
	if a.chat.Agent.Removed {
		chat = primaryConversation(a.directory)
	}
	v.draft = tasks.Watch{ConversationID: chat, Mode: tasks.WatchDigest}
	v.budget, v.err = "15", ""
	a.selectWatchConnection(connection)
	if v.draft.Scope == "firing" {
		v.draft.Name = connection.Name + " alerts"
	}
}

func (a *desktop) selectWatchConnection(connection tasks.ProviderConnection) {
	draft := &a.autonomy.draft
	draft.ConnectionID, draft.Source, draft.Scope = connection.ID, connection.Source, ""
	draft.Filters = nil
	if connection.Source == tasks.GrafanaAlerts || connection.Source == tasks.PrometheusAlerts {
		draft.Scope = "firing"
	}
}

func watchFilterName(field string) string {
	switch field {
	case "branch":
		return "Branch"
	case "workflow":
		return "Workflow"
	case "sender":
		return "Sender email"
	case "subject":
		return "Subject contains"
	case "alert":
		return "Alert name"
	case "severity":
		return "Severity"
	default:
		return "Text contains"
	}
}

func watchFilterFields(c *ui.Context, watch *tasks.Watch) {
	fields := []string{"text"}
	switch watch.Source {
	case tasks.GitHubCI:
		fields = append(fields, "branch", "workflow")
	case tasks.GmailLabel:
		fields = append(fields, "sender", "subject")
	case tasks.GrafanaAlerts, tasks.PrometheusAlerts:
		fields = append(fields, "alert", "severity")
	}
	if len(watch.Filters) != 0 {
		ui.Text(c, "All of these filters must match").FontSize(12).TextColor(c.Theme().TextMuted)
	}
	for i := range watch.Filters {
		ui.Row(c.Key(i)).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.MenuButton(c.Key("filter-field"), watchFilterName(watch.Filters[i].Field), func(m *ui.Menu) {
				for _, field := range fields {
					if m.Item(watchFilterName(field)).Chosen() {
						watch.Filters[i].Field = field
					}
				}
			})
			ui.TextInput(c.Key("filter-value"), &watch.Filters[i].Value).Label(watchFilterName(watch.Filters[i].Field)).
				Placeholder("Match value").Grow(1).MinWidth(0)
			ui.Button(c.Key("filter-remove"), "Remove").OnClick(func() {
				watch.Filters = slices.Delete(watch.Filters, i, i+1)
			})
		})
	}
	if len(watch.Filters) < 4 {
		ui.Button(c.Key("add-filter"), "Add filter").AlignSelf(ui.Start).OnClick(func() {
			watch.Filters = append(watch.Filters, tasks.WatchFilter{Field: fields[0]})
		})
	}
}

func (a *desktop) watchConnectionLabel(source, id string) string {
	if id == "" {
		return providerName(source) + " · Server configured"
	}
	if connection := a.integration(id); connection.ID != "" {
		return providerName(source) + " · " + connection.Name
	}
	return providerName(source) + " · Disconnected"
}

func (a *desktop) watchScopeFields(c *ui.Context) {
	v := &a.autonomy
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Connection").TextColor(c.Theme().TextMuted)
		label := a.watchConnectionLabel(v.draft.Source, v.draft.ConnectionID)
		if v.draft.ID != "" {
			ui.Text(c, label).FontWeight(620)
		} else {
			ui.MenuButton(c.Key("watch-connection"), label, func(menu *ui.Menu) {
				for _, choice := range a.availableWatchConnections() {
					label := providerName(choice.Source) + " · " + choice.Name
					if detail := strings.TrimSpace(choice.Account + " " + choice.Endpoint); detail != "" {
						label += " · " + detail
					}
					if menu.Item(label).Chosen() {
						a.selectWatchConnection(choice)
					}
				}
			})
		}
	})
	if v.draft.Source == tasks.GrafanaAlerts || v.draft.Source == tasks.PrometheusAlerts {
		ui.Text(c, "Source · All firing alerts").FontSize(12).TextColor(c.Theme().TextMuted)
		return
	}
	placeholder, browse, label := "owner/repository", "Browse recent repositories", "Repository"
	if v.draft.Source == tasks.GmailLabel {
		placeholder, browse, label = "INBOX or label ID", "Choose Gmail label", "Gmail label ID"
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c.Key("watch-scope"), &v.draft.Scope).Label(label).Placeholder(placeholder).
			Grow(1).MinWidth(0).Disabled(v.draft.ID != "")
		if v.draft.ID == "" && v.draft.ConnectionID != "" {
			a.loadWatchScopes(v.draft.ConnectionID)
			if len(v.scopes.options) != 0 && v.scopes.connection == v.draft.ConnectionID {
				ui.MenuButton(c.Key("watch-source-list"), browse, func(menu *ui.Menu) {
					for _, option := range v.scopes.options {
						if menu.Item(option.Name).Chosen() {
							v.draft.Scope = option.ID
							if strings.TrimSpace(v.draft.Name) == "" {
								v.draft.Name = option.Name
							}
						}
					}
				})
			}
		}
	})
	if v.draft.ID == "" && v.draft.ConnectionID != "" && v.scopes.connection == v.draft.ConnectionID {
		if v.scopes.loading {
			ui.Text(c, "Loading sources…").FontSize(12).TextColor(c.Theme().TextMuted)
		} else if v.scopes.err != "" {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.Text(c, v.scopes.err).FontSize(12).TextColor(c.Theme().TextMuted)
				ui.Button(c.Key("retry-watch-sources"), "Retry").OnClick(func() { v.scopes = watchScopes{} })
			})
		}
	}
}

func (a *desktop) loadWatchScopes(id string) {
	v := &a.autonomy
	if a.window == nil || !a.online || v.scopes.connection == id {
		return
	}
	v.scopes = watchScopes{connection: id, loading: true}
	win, client := a.window, a.client
	go func() {
		options, err := client.ProviderScopes(a.ctx, id)
		win.Update(func() {
			if v.scopes.connection != id {
				return
			}
			v.scopes.loading = false
			if err != nil {
				v.scopes.err = errorMessage(err, "Couldn’t load sources. You can enter the ID directly.")
			} else {
				v.scopes.options = options
			}
		})
	}()
}
