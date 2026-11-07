package main

import (
	"testing"

	"buddy/internal/api"
	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func TestMCPSetupExposesTransportFields(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.MCPProtocol}
	view := ui.NewTester(a.mcpSettings, 740, 680)
	if err := view.Click("Add MCP server"); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Find("MCP server URL"); !ok {
		t.Fatal("HTTP setup is missing its URL")
	}
	if err := view.Click("Local command"); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Find("Executable on Buddy server"); !ok {
		t.Fatal("STDIO setup is missing its command")
	}
	if _, ok := view.Find("Bearer token (optional)"); ok {
		t.Fatal("STDIO setup showed HTTP authentication")
	}
	if err := view.Click("Tool access and environment"); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Find("Allowed tools, one per line"); !ok {
		t.Fatal("MCP setup is missing its tool allowlist")
	}
	if _, ok := view.Find("Preapprove listed tools"); !ok {
		t.Fatal("MCP setup is missing explicit tool approval")
	}
}

func TestRemovedMCPServerLeavesEditorCancelable(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.MCPProtocol}
	a.editMCP(tasks.MCPServer{ID: "removed", Name: "Removed", Transport: tasks.MCPHTTP, URL: "https://tools.example.com/mcp"})
	view := ui.NewTester(a.mcpSettings, 740, 680)
	if _, ok := view.Find("This server was removed. Cancel to return to the server list."); !ok {
		t.Fatal("editor did not show that the server was removed")
	}
	if err := view.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Add MCP server"); err != nil {
		t.Fatal(err)
	}
}
