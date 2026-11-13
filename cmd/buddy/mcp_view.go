package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type mcpView struct {
	servers                    []tasks.MCPServer
	draft                      *mcpDraft
	busy, notice, err, loadErr string
	loading                    bool
	next                       time.Time
	revision                   uint64
}

type mcpDraft struct {
	server                       tasks.MCPServer
	arguments, env, tools, token string
	clearToken, advanced         bool
}

func (a *desktop) refreshMCP(c *ui.Context) {
	v := &a.mcp
	if !a.online || a.serverProtocol < api.MCPProtocol || v.loading || v.busy != "" || a.window == nil {
		return
	}
	if wait := time.Until(v.next); wait > 0 {
		c.After(wait)
		return
	}
	v.loading, v.next = true, time.Now().Add(8*time.Second)
	win, client, revision := a.window, a.client, v.revision
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
		defer cancel()
		servers, err := client.MCPServers(ctx)
		win.Update(func() {
			v.loading = false
			if v.revision != revision {
				return
			}
			if err != nil {
				v.loadErr = errorMessage(err, "Couldn't load MCP servers.")
				return
			}
			v.servers, v.loadErr = servers, ""
		})
	}()
}

func (a *desktop) mcpSettings(c *ui.Context) {
	v := &a.mcp
	a.refreshMCP(c)
	sectionLabel(c, "MCP servers")
	connectionSection(c, func() {
		ui.Row(c).Padding(11, 13).Gap(8).AlignItems(ui.Center).Children(func() {
			connectionDetail(c, linkMark, "Custom tools", "")
			ui.Button(c.Key("add-mcp"), "Add MCP server").Disabled(!a.online || a.serverProtocol < api.MCPProtocol || v.busy != "" || v.draft != nil).
				OnClick(func() { a.editMCP(tasks.MCPServer{Transport: tasks.MCPHTTP}) })
		})
		switch {
		case !a.online:
			mcpNote(c, "Connect to your Buddy server to manage MCP tools.")
		case a.serverProtocol < api.MCPProtocol:
			mcpNote(c, "Update the Buddy server to add custom MCP tools.")
		}
		if v.loadErr != "" {
			ui.Text(c, v.loadErr).Padding(8, 13).FontSize(12).TextColor(c.Theme().Danger)
		}
		if v.err != "" && v.draft == nil {
			ui.Text(c, v.err).Padding(8, 13).FontSize(12).TextColor(c.Theme().Danger)
		}
		if v.notice != "" {
			mcpNote(c, v.notice)
		}
		if v.draft != nil {
			id := v.draft.server.ID
			removed := id != "" && !slices.ContainsFunc(v.servers, func(server tasks.MCPServer) bool { return server.ID == id })
			a.mcpForm(c.Key("mcp-editor"), removed)
		}
		for _, server := range v.servers {
			a.mcpRow(c.Key(server.ID), server)
		}
	})
}

func mcpNote(c *ui.Context, text string) {
	ui.Text(c, text).Padding(8, 13).FontSize(12).TextColor(c.Theme().TextMuted)
}

func (a *desktop) editMCP(server tasks.MCPServer) {
	a.mcp.err, a.mcp.notice = "", ""
	a.mcp.draft = &mcpDraft{
		server: server, arguments: strings.Join(server.Args, "\n"), env: strings.Join(server.EnvVars, "\n"),
		tools: strings.Join(server.Tools, "\n"), advanced: len(server.Tools)+len(server.EnvVars) != 0,
	}
}

func (a *desktop) mcpRow(c *ui.Context, server tasks.MCPServer) {
	v := &a.mcp
	disabled := !a.online || v.busy != "" || v.draft != nil
	detail := "Streamable HTTP · " + server.URL
	if server.Transport == tasks.MCPStdio {
		detail = "Local command · " + server.Command
	}
	status := "Disabled"
	if server.NeedsReview {
		status = "Review required"
	} else if server.Enabled {
		status = "Enabled"
	}
	ui.Row(c).Padding(8, 13).Gap(8).AlignItems(ui.Center).Children(func() {
		connectionDetail(c, linkMark, server.Name, detail)
		connectionStatus(c, status, server.Enabled && !server.NeedsReview)
		label := "Test"
		if v.busy == "test:"+server.ID {
			label = "Testing…"
		}
		ui.Button(c.Key("test-mcp"), label).Label("Test " + server.Name).Disabled(disabled).OnClick(func() {
			a.runMCPAction("test:"+server.ID, func(ctx context.Context, client *api.Client) (string, error) {
				result, err := client.TestMCPServer(ctx, server.ID)
				if err != nil {
					return "", err
				}
				n := len(result.Tools)
				message := fmt.Sprintf("%s connected · %d callable tools", server.Name, n)
				if n > 0 {
					message += " · " + strings.Join(result.Tools[:min(n, 5)], ", ")
					if n > 5 {
						message += ", …"
					}
				}
				if blocked := len(result.RequiresApproval); blocked > 0 {
					message += fmt.Sprintf(" · %d require approval: %s", blocked, strings.Join(result.RequiresApproval[:min(blocked, 5)], ", "))
					if blocked > 5 {
						message += ", …"
					}
				}
				return message, nil
			})
		})
		ui.Button(c.Key("manage-mcp"), "⋯").Label("Manage " + server.Name).Disabled(disabled).PopoverMenuButton(func(menu *ui.Menu) {
			if menu.Item("Edit").Chosen() {
				a.editMCP(server)
			}
			toggle := "Enable"
			if server.Enabled {
				toggle = "Disable"
			}
			if menu.Item(toggle).Chosen() {
				a.runMCPAction(server.ID, func(ctx context.Context, client *api.Client) (string, error) {
					_, err := client.SetMCPServerEnabled(ctx, server.ID, !server.Enabled)
					return "", err
				})
			}
			if menu.Item("Remove").Chosen() {
				a.removeMCP(server)
			}
		})
	})
}

func (a *desktop) mcpForm(c *ui.Context, removed bool) {
	v, draft := &a.mcp, a.mcp.draft
	busy := v.busy != ""
	ui.Column(c).Padding(13).Gap(9).Children(func() {
		title := "Add MCP server"
		if draft.server.ID != "" {
			title = "Edit MCP server"
		}
		ui.Text(c, title).FontWeight(680).Role(ui.RoleHeading)
		if removed {
			ui.Text(c, "This server was removed. Cancel to return to the server list.").FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.TextInput(c.Key("mcp-name"), &draft.server.Name).Label("MCP server name").Placeholder("Name, e.g. docs-tools").Disabled(busy)
		ui.Row(c).Gap(8).Children(func() {
			for _, transport := range []struct{ value, label string }{
				{tasks.MCPHTTP, "Streamable HTTP"}, {tasks.MCPStdio, "Local command"},
			} {
				button := ui.Button
				if draft.server.Transport == transport.value {
					button = ui.PrimaryButton
				}
				button(c.Key("mcp-"+transport.value), transport.label).Disabled(busy).OnClick(func() {
					draft.server.Transport = transport.value
				})
			}
		})
		if draft.server.Transport == tasks.MCPHTTP {
			ui.TextInput(c.Key("mcp-url"), &draft.server.URL).Label("MCP server URL").
				Placeholder("https://tools.example.com/mcp").Disabled(busy)
			label := "Bearer token (optional)"
			if draft.server.HasToken {
				label = "Replace saved bearer token"
			}
			ui.TextInput(c.Key("mcp-token"), &draft.token).Password().Label(label).Placeholder(label).Disabled(busy || draft.clearToken)
			if draft.server.HasToken {
				ui.Checkbox(c.Key("mcp-clear-token"), &draft.clearToken, "Clear saved token").Disabled(busy)
			}
			ui.Text(c, "Use a URL reachable from the Buddy server. Changing the URL clears the saved token unless you enter a new one.").
				FontSize(12).TextColor(c.Theme().TextMuted)
		} else {
			ui.TextInput(c.Key("mcp-command"), &draft.server.Command).Label("Executable on Buddy server").
				Placeholder("e.g. npx or /opt/tools/my-mcp").Disabled(busy)
			ui.TextArea(c.Key("mcp-arguments"), &draft.arguments).Label("Arguments, one per line").Height(65).
				Placeholder("Optional arguments, one per line").Disabled(busy)
			ui.Text(c, "This executable runs with your Buddy server account’s access. Install it outside Bud workspaces. Use absolute paths to scripts that already exist, and review and save again after updates.").
				FontSize(12).TextColor(c.Theme().TextMuted)
		}
		ui.Text(c, "Enable servers you trust. Buds can use their tools within the permissions those servers provide.").
			FontSize(12).TextColor(c.Theme().TextMuted)
		advanced := "Tool access and environment"
		if draft.advanced {
			advanced = "Hide advanced options"
		}
		ui.Button(c.Key("mcp-advanced"), advanced).OnClick(func() { draft.advanced = !draft.advanced })
		if draft.advanced {
			ui.TextArea(c.Key("mcp-tools"), &draft.tools).Label("Allowed tools, one per line").
				Placeholder("Allowed tool names, one per line. Blank allows all.").Height(60).Disabled(busy)
			ui.Checkbox(c.Key("mcp-approve"), &draft.server.ApproveTools, "Preapprove listed tools").Disabled(busy)
			ui.Text(c, "By default, Buds can call tools that declare themselves read-only. Preapproval lets Buds call the listed tools without a separate confirmation. Some tools can change external data.").
				FontSize(12).TextColor(c.Theme().TextMuted)
			if draft.server.Transport == tasks.MCPStdio {
				ui.TextArea(c.Key("mcp-env"), &draft.env).Label("Server environment variable names, one per line").
					Placeholder("Existing server environment variable names, one per line").Height(60).Disabled(busy)
				ui.Text(c, "Buddy reserves OPENAI_API_KEY and CODEX_API_KEY for Codex.").
					FontSize(12).TextColor(c.Theme().TextMuted)
			}
		}
		if v.err != "" {
			ui.Text(c, v.err).FontSize(12).TextColor(c.Theme().Danger).Selectable()
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c.Key("cancel-mcp"), "Cancel").Disabled(busy).OnClick(func() { v.draft, v.err = nil, "" })
			label := "Save and enable"
			if draft.server.ID != "" {
				label = "Save changes"
			}
			if busy {
				label = "Saving…"
			}
			value := draft.server.URL
			if draft.server.Transport == tasks.MCPStdio {
				value = draft.server.Command
			}
			ui.PrimaryButton(c.Key("save-mcp"), label).
				Disabled(busy || removed || !a.online || strings.TrimSpace(draft.server.Name) == "" || strings.TrimSpace(value) == "" ||
					(draft.server.ApproveTools && strings.TrimSpace(draft.tools) == "")).
				OnClick(a.saveMCP)
		})
	})
}

func mcpLines(text string, trim bool) []string {
	var lines []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if trim {
			line = strings.TrimSpace(line)
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (a *desktop) saveMCP() {
	draft := a.mcp.draft
	if draft == nil {
		return
	}
	s := draft.server
	input := tasks.MCPServerSetup{Name: s.Name, Transport: s.Transport, URL: s.URL, Command: s.Command,
		Args: mcpLines(draft.arguments, false), EnvVars: mcpLines(draft.env, true), Tools: mcpLines(draft.tools, true),
		ApproveTools: s.ApproveTools}
	if s.Transport == tasks.MCPHTTP && (draft.clearToken || strings.TrimSpace(draft.token) != "") {
		token := draft.token
		if draft.clearToken {
			token = ""
		}
		input.Token = &token
	}
	a.runMCPAction("save", func(ctx context.Context, client *api.Client) (string, error) {
		_, err := client.SaveMCPServer(ctx, s.ID, input)
		return "", err
	})
}

func (a *desktop) removeMCP(server tasks.MCPServer) {
	win := a.window
	a.runMCPAction(server.ID, func(ctx context.Context, client *api.Client) (string, error) {
		choice, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "Remove MCP server",
			Message: "Remove " + server.Name + "?",
			Detail:  "Buddy deletes its saved configuration and token. New runs will no longer use these tools.",
			Buttons: []string{"Cancel", "Remove"}, DestructiveButtons: []int{1},
		})
		if err != nil || choice.Button != 1 {
			return "", err
		}
		return "", client.DeleteMCPServer(ctx, server.ID)
	})
}

func (a *desktop) runMCPAction(key string, action func(context.Context, *api.Client) (string, error)) {
	v := &a.mcp
	if !a.online || v.busy != "" || a.window == nil {
		return
	}
	v.busy, v.err, v.notice = key, "", ""
	v.revision++
	win, client := a.window, a.client
	go func() {
		notice, err := action(a.ctx, client)
		win.Update(func() {
			v.busy = ""
			v.next = time.Time{}
			if err != nil {
				v.err = errorMessage(err, "Couldn't update the MCP server.")
				if errors.Is(err, context.DeadlineExceeded) {
					v.err = "Buddy didn't respond in time. Check the connection and try again."
				}
				return
			}
			if key == "save" {
				v.draft = nil
			}
			v.notice = notice
		})
	}()
}
