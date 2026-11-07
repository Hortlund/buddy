package main

import (
	"context"
	"net/url"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type integrationsView struct {
	catalog       api.ConnectionCatalog
	bundledGoogle api.GoogleOAuthClient
	draft         *api.ConnectionSetup
	editing       string
	busy          string
	cancel        context.CancelFunc
	revision      uint64
	loading       bool
	next          time.Time
	loadErr       string
	err           string
	notice        string
	showPaused    bool
	manualGitHub  bool
	googleSetup   bool
}

var integrationProviders = []struct{ source, name, detail string }{
	{tasks.GitHubCI, "GitHub", "Work with issues, pull requests, and CI from chat."},
	{tasks.GmailLabel, "Gmail", "Search and preview messages in chat. Watch labels in selected mailboxes."},
	{tasks.GrafanaAlerts, "Grafana", "Find dashboards and check or watch firing alerts."},
	{tasks.PrometheusAlerts, "Prometheus", "Run instant PromQL queries and check or watch firing alerts."},
}

func providerName(source string) string {
	for _, provider := range integrationProviders {
		if provider.source == source {
			return provider.name
		}
	}
	return source
}

func (a *desktop) refreshIntegrations(c *ui.Context) {
	v := &a.integrations
	if !a.online || a.serverProtocol < api.IntegrationsProtocol || v.loading || a.window == nil {
		return
	}
	if wait := time.Until(v.next); wait > 0 {
		c.After(wait)
		return
	}
	v.loading, v.next = true, time.Now().Add(8*time.Second)
	win, client, revision := a.window, a.client, v.revision
	go func() {
		catalog, err := client.Integrations(a.ctx)
		win.Update(func() {
			v.loading = false
			if v.revision != revision {
				return
			}
			if err != nil {
				v.loadErr = errorMessage(err, "Couldn’t load integrations.")
				return
			}
			v.catalog, v.loadErr = catalog, ""
		})
	}()
}

func (a *desktop) integrationSettings(c *ui.Context) {
	v := &a.integrations
	a.refreshIntegrations(c)
	sectionLabel(c, "Integrations")
	if a.serverProtocol < api.IntegrationsProtocol {
		ui.Text(c, "Connect to an up-to-date Buddy server to manage integrations.").TextColor(c.Theme().TextMuted)
		return
	}
	messages := []string{v.loadErr}
	if v.draft == nil {
		messages = append(messages, v.err)
	}
	for _, message := range messages {
		if message != "" {
			ui.Text(c, message).TextColor(c.Theme().Danger).Selectable()
		}
	}
	if v.notice != "" {
		ui.Text(c, v.notice).TextColor(c.Theme().TextMuted)
	}
	paused := 0
	for _, provider := range integrationProviders {
		connectionSection(c.Key(provider.source), func() {
			ui.Row(c).Padding(10, 13).Gap(8).AlignItems(ui.Center).Children(func() {
				connectionDetail(c, linkMark, provider.name, provider.detail)
				label := "Add"
				accessibilityLabel := "Add " + provider.name
				if a.serverProtocol >= api.IntegrationSignInProtocol && provider.source == tasks.GmailLabel {
					label = "Set up Gmail"
					if a.googleSignInAvailable() {
						label = "Continue with Google"
					}
					accessibilityLabel = label
				}
				services := c.Services()
				ui.Button(c.Key("add-"+provider.source), label).Label(accessibilityLabel).
					Disabled(!a.online || v.busy != "" || v.draft != nil).OnClick(func() {
					a.openIntegration(services, tasks.ProviderConnection{Source: provider.source})
				})
				if provider.source == tasks.GmailLabel && v.catalog.GoogleSignIn {
					ui.Button(c.Key("gmail-sign-in-setup"), "⋯").Label("Google sign-in settings").
						Disabled(!a.online || v.busy != "" || v.draft != nil).
						PopoverMenuButton(func(menu *ui.Menu) {
							if menu.Item("Replace Google OAuth client…").Chosen() {
								a.beginIntegration(tasks.ProviderConnection{Source: tasks.GmailLabel})
								v.googleSetup = true
							}
						})
				}
			})
			if v.draft != nil && v.draft.Source == provider.source && v.editing == "" {
				a.integrationForm(c.Key("add-integration"))
			}
			for _, connection := range v.catalog.Connections {
				if connection.Source != provider.source {
					continue
				}
				if !connection.Connected {
					paused++
					if !v.showPaused && (v.draft == nil || v.editing != connection.ID) {
						continue
					}
				}
				a.integrationRow(c.Key(connection.ID), connection)
				if v.draft != nil && v.editing == connection.ID {
					a.integrationForm(c.Key("reconnect-" + connection.ID))
				}
			}
		})
	}
	if paused > 0 {
		label := "Show disconnected"
		if v.showPaused {
			label = "Hide disconnected"
		}
		ui.Button(c.Key("disconnected-integrations"), label).AlignSelf(ui.Start).
			OnClick(func() { v.showPaused = !v.showPaused })
	}
	if v.catalog.LegacyGitHub || v.catalog.LegacyGmail {
		ui.Text(c, "Existing server-configured watches still use automations.json.").TextColor(c.Theme().TextMuted)
	}
}

func (a *desktop) beginIntegration(c tasks.ProviderConnection) {
	v := &a.integrations
	v.draft = &api.ConnectionSetup{Name: c.Name, Source: c.Source, Endpoint: c.Endpoint,
		Collaboration: a.serverProtocol >= api.IntegrationSignInProtocol && c.Source == tasks.GitHubCI && (c.ID == "" || c.Collaboration)}
	v.editing, v.err, v.notice = c.ID, "", ""
	v.manualGitHub = !v.catalog.GitHubCLI
	v.googleSetup = false
}

func (a *desktop) openIntegration(services ui.Services, connection tasks.ProviderConnection) {
	a.beginIntegration(connection)
	if connection.Source == tasks.GmailLabel && a.googleSignInAvailable() {
		a.connectIntegration(services)
	}
}

func (a *desktop) googleSignInAvailable() bool {
	return a.serverProtocol >= api.IntegrationSignInProtocol &&
		(a.integrations.catalog.GoogleSignIn || a.integrations.bundledGoogle.ID != "")
}

func (a *desktop) integrationRow(c *ui.Context, connection tasks.ProviderConnection) {
	v := &a.integrations
	detail := strings.TrimSpace(connection.Account + " " + connection.Endpoint)
	status := "Connected"
	if !connection.Connected {
		status = "Disconnected"
	} else if connection.Problem != "" {
		status = "Needs attention"
	}
	if detail != "" {
		detail += " · "
	}
	if connection.Connected && connection.Source == tasks.GitHubCI {
		if connection.Collaboration {
			status += " · Collaboration"
		} else {
			status += " · Read only"
		}
	}
	ui.Column(c).Padding(8, 13).Gap(5).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Text(c, connection.Name).FontWeight(650).SingleLine()
				ui.Text(c, detail+status).FontSize(12).SingleLine().TextColor(c.Theme().TextMuted)
			})
			disabled := !a.online || v.busy != "" || v.draft != nil
			if connection.Connected {
				services := c.Services()
				ui.Button(c.Key("watch-"+connection.ID), "Add watch").Disabled(disabled).OnClick(func() {
					a.startIntegrationWatch(connection)
				})
				ui.Button(c.Key("manage-"+connection.ID), "⋯").Label("Manage " + connection.Name).Disabled(disabled).
					PopoverMenuButton(func(menu *ui.Menu) {
						if menu.Item("Test connection").Chosen() {
							a.runIntegrationAction(connection.ID, func(ctx context.Context, client *api.Client) (string, error) {
								checked, err := client.CheckProvider(ctx, connection.ID)
								if err != nil {
									return "", err
								}
								if checked.Problem != "" {
									return "", &tasks.InputError{Message: checked.Problem}
								}
								return connection.Name + " passed its connection test.", nil
							})
						}
						if menu.Item("Reconnect").Chosen() {
							a.openIntegration(services, connection)
						}
						if connection.Source == tasks.GitHubCI && !connection.Collaboration &&
							a.serverProtocol >= api.IntegrationSignInProtocol && menu.Item("Enable collaboration…").Chosen() {
							a.beginIntegration(connection)
							v.draft.Collaboration = true
						}
						if menu.Item("Disconnect").Chosen() {
							a.disconnectIntegration(connection)
						}
					})
			} else {
				services := c.Services()
				ui.Button(c.Key("reconnect-"+connection.ID), "Reconnect").Disabled(disabled).
					OnClick(func() { a.openIntegration(services, connection) })
			}
		})
		if connection.Problem != "" {
			ui.Text(c, connection.Problem).FontSize(12).TextColor(c.Theme().Danger)
		}
	})
}

func (a *desktop) integrationForm(c *ui.Context) {
	v, draft := &a.integrations, a.integrations.draft
	connectionSection(c, func() {
		ui.Column(c).Padding(13).Gap(9).Children(func() {
			title := "Connect " + providerName(draft.Source)
			if v.editing != "" {
				title = "Reconnect " + providerName(draft.Source)
			}
			ui.Text(c, title).Role(ui.RoleHeading).FontWeight(680)
			if v.err != "" {
				ui.Text(c, v.err).FontSize(12).TextColor(c.Theme().Danger).Selectable()
			}
			busy := v.busy != ""
			modern := a.serverProtocol >= api.IntegrationSignInProtocol
			services := c.Services()
			needsGoogleSetup := draft.Source == tasks.GmailLabel && (!a.googleSignInAvailable() || v.googleSetup) && draft.ClientID == ""
			if !modern || (draft.Source != tasks.GmailLabel && draft.Source != tasks.GitHubCI) {
				ui.TextInput(c.Key("integration-name"), &draft.Name).Label("Connection name").
					Placeholder("e.g. Work or Production").Disabled(busy)
			}
			switch draft.Source {
			case tasks.GmailLabel:
				if needsGoogleSetup {
					gmailSetupSteps(c, busy)
				} else {
					ui.Text(c, "Choose the mailbox in your browser and grant Buddy read access.").FontSize(12).TextColor(c.Theme().TextMuted)
				}
			case tasks.GitHubCI:
				if modern {
					ui.Checkbox(c.Key("github-collaboration"), &draft.Collaboration, "Allow reviewed GitHub writes").Disabled(busy)
				}
				if modern && v.catalog.GitHubCLI && !v.manualGitHub {
					ui.Text(c, "Reuse the GitHub CLI account signed in on your Buddy server.").FontSize(12).TextColor(c.Theme().TextMuted)
					ui.Button(c.Key("github-use-token"), "Use a token instead").AlignSelf(ui.Start).Disabled(busy).
						OnClick(func() { v.manualGitHub, v.err = true, "" })
				} else {
					ui.Text(c, "Choose the repositories Buddy should access when creating the token.").FontSize(12).TextColor(c.Theme().TextMuted)
					if modern {
						ui.TextInput(c.Key("integration-name"), &draft.Name).Label("Connection name").
							Placeholder("Connection name (optional)").Disabled(busy)
					}
					ui.TextInput(c.Key("integration-token"), &draft.Token).Password().Label("GitHub token").
						Placeholder("Paste a GitHub token").Disabled(busy)
					ui.Button(c.Key("github-token-link"), "Create a GitHub token").AlignSelf(ui.Start).
						OnClick(func() { services.OpenURL(githubTokenSetupURL(draft.Collaboration)) })
				}
			default:
				ui.TextInput(c.Key("integration-endpoint"), &draft.Endpoint).Label("Instance URL").
					Placeholder("https://monitoring.example.com").Disabled(busy || v.editing != "")
				placeholder := "Service account token"
				if draft.Source == tasks.PrometheusAlerts {
					placeholder = "Bearer token (optional)"
				}
				ui.TextInput(c.Key("integration-token"), &draft.Token).Password().Label(placeholder).
					Placeholder(placeholder).Disabled(busy)
				ui.Text(c, "Use a URL reachable from your Buddy server.").FontSize(12).TextColor(c.Theme().TextMuted)
			}
			canSave := strings.TrimSpace(draft.Name) != "" && !busy && a.online
			label := "Test and connect"
			cli := modern && draft.Source == tasks.GitHubCI && v.catalog.GitHubCLI && !v.manualGitHub
			switch draft.Source {
			case tasks.GmailLabel:
				label, canSave = "Continue with Google", !busy && a.online &&
					(a.googleSignInAvailable() && !v.googleSetup || (!modern && draft.ClientID != "" && strings.TrimSpace(draft.Name) != ""))
			case tasks.GitHubCI:
				canSave = !busy && a.online && (modern || strings.TrimSpace(draft.Name) != "") &&
					(cli || strings.TrimSpace(draft.Token) != "")
				if cli {
					label = "Use GitHub CLI sign-in"
				}
			default:
				canSave = canSave && strings.TrimSpace(draft.Endpoint) != "" &&
					(draft.Source == tasks.PrometheusAlerts || strings.TrimSpace(draft.Token) != "")
			}
			if busy && v.busy != "import" {
				label = "Connecting…"
				if draft.Source == tasks.GmailLabel {
					label = "Waiting for Google…"
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c.Key("cancel-integration"), "Cancel").Disabled(busy && v.cancel == nil).OnClick(func() {
					if v.cancel != nil {
						v.cancel()
					}
					v.draft, v.editing, v.err = nil, "", ""
				})
				if needsGoogleSetup {
					importLabel := "Import OAuth client JSON"
					if busy {
						importLabel = "Importing…"
					} else if modern && !v.googleSetup {
						importLabel = "Import JSON and sign in"
					}
					ui.PrimaryButton(c.Key("oauth-import"), importLabel).Disabled(busy || !a.online).
						OnClick(func() { a.chooseGmailClient(services) })
				}
				if !needsGoogleSetup || !modern {
					ui.PrimaryButton(c.Key("save-integration"), label).Disabled(!canSave).
						OnClick(func() {
							draft.UseGitHubCLI = cli
							a.connectIntegration(services)
						})
				}
			})
		})
	})
}

func githubTokenSetupURL(collaborate bool) string {
	access := "read"
	if collaborate {
		access = "write"
	}
	query := url.Values{"name": {"Buddy"}, "description": {"Buddy repository collaboration and CI"},
		"contents": {"read"}, "actions": {"read"}, "issues": {access}, "pull_requests": {access}}
	return "https://github.com/settings/personal-access-tokens/new?" + query.Encode()
}

func (a *desktop) finishIntegrationChange() {
	v := &a.integrations
	v.revision++
	v.next = time.Time{}
	a.autonomy.next = time.Time{}
}

func (a *desktop) runIntegrationAction(id string, action func(context.Context, *api.Client) (string, error)) {
	v := &a.integrations
	if v.busy != "" || a.window == nil || !a.online {
		return
	}
	v.busy, v.err, v.notice = id, "", ""
	win, client := a.window, a.client
	go func() {
		notice, err := action(a.ctx, client)
		win.Update(func() {
			v.busy = ""
			if err != nil {
				v.err = errorMessage(err, "Couldn’t update the connection.")
			} else {
				v.notice = notice
			}
			a.finishIntegrationChange()
		})
	}()
}

func (a *desktop) disconnectIntegration(connection tasks.ProviderConnection) {
	win := a.window
	a.runIntegrationAction(connection.ID, func(ctx context.Context, client *api.Client) (string, error) {
		choice, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "Disconnect integration",
			Message: "Disconnect " + connection.Name + "?",
			Detail:  "Buddy removes the saved credential and pauses its watches. Existing results and jobs are kept.",
			Buttons: []string{"Cancel", "Disconnect"}, DestructiveButtons: []int{1},
		})
		if err != nil || choice.Button != 1 {
			return "", err
		}
		return "Disconnected " + connection.Name + ". Its watches are paused.", client.DisconnectProvider(ctx, connection.ID)
	})
}
