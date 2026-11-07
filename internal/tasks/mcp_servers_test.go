package tasks

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMCPConfigScopeAndCredentials(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := "fixture-token"
	input := MCPServerSetup{Name: "docs-tools", Transport: MCPHTTP, URL: "https://mcp.example/mcp",
		Token: &token, Tools: []string{"lookup"}}
	server, err := store.SaveMCPServer(ctx, "", input)
	if err != nil || !server.Enabled || !server.HasToken {
		t.Fatalf("save: %+v, %v", server, err)
	}
	input.Token = nil
	if updated, err := store.SaveMCPServer(ctx, server.ID, input); err != nil || !updated.HasToken {
		t.Fatalf("same endpoint should retain its token: %+v, %v", updated, err)
	}

	runtime := &CodexRuntime{MCPStore: store}
	for _, turn := range []Turn{{DeviceExecution: true}, {WatchSignal: &StandingSignal{}}} {
		got, _, err := runtime.mcpForRun(ctx, turn)
		if err != nil || len(got) != 0 {
			t.Fatalf("unexpected MCP access: %d configurations, %v", len(got), err)
		}
	}
	enabled, _, err := runtime.mcpForRun(ctx, Turn{})
	if err != nil || len(enabled) != 1 {
		t.Fatalf("home turn got %d configurations, %v", len(enabled), err)
	}
	home := t.TempDir()
	if err := writeMCPConfig(home, enabled); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("MCP config should be private: %v", err)
	}
	if !strings.Contains(string(data), `http_headers = { Authorization = "Bearer fixture-token" }`) ||
		!strings.Contains(string(data), `enabled_tools = ["lookup"]`) ||
		!strings.Contains(string(data), `default_tools_approval_mode = "writes"`) ||
		strings.Contains(string(data), "mcp_optional_startup_grace_ms") {
		t.Fatal("Codex config lost its credential or tool allowlist")
	}
	input.URL = "https://other.example/mcp"
	if updated, err := store.SaveMCPServer(ctx, server.ID, input); err != nil || updated.HasToken {
		t.Fatalf("token must not follow the old endpoint: %+v, %v", updated, err)
	}
	if _, err := store.SetMCPServerEnabled(ctx, server.ID, false); err != nil {
		t.Fatal(err)
	}
	if enabled, _, err := runtime.mcpForRun(ctx, Turn{}); err != nil || len(enabled) != 0 {
		t.Fatalf("disabled server remained active: %d configurations, %v", len(enabled), err)
	}
}

func TestMCPApprovalRequiresExactToolAllowlist(t *testing.T) {
	input := MCPServerSetup{Name: "fixture", Transport: MCPHTTP, URL: "https://mcp.example/mcp", ApproveTools: true}
	if err := input.validate(); err == nil {
		t.Fatal("preapproved an unrestricted server")
	}
	input.Tools = []string{"lookup", "lookup"}
	if err := input.validate(); err == nil {
		t.Fatal("accepted duplicate tool names")
	}
	input.Tools = []string{"lookup"}
	store, err := OpenStore(t.Context(), filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := store.SaveMCPServer(t.Context(), "", input)
	if err != nil || !server.ApproveTools {
		t.Fatalf("save: %+v, %v", server, err)
	}
	configs, err := store.mcpServers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := writeMCPConfig(home, configs); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || !strings.Contains(string(config), "[mcp_servers.\"fixture\".tools.\"lookup\"]\napproval_mode = \"approve\"") {
		t.Fatalf("missing per-tool approval: %s, %v", config, err)
	}
}

func TestMCPLaunchRequiresExistingUnlinkedFiles(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	install := t.TempDir()
	launcher := filepath.Join(install, "server")
	script := filepath.Join(install, "server.py")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	input := MCPServerSetup{Name: "local", Transport: MCPStdio, Command: launcher, Args: []string{"--script=" + script}}
	if _, err := store.SaveMCPServer(ctx, "", input); err == nil {
		t.Fatal("approved a script that didn't exist")
	}
	if err := os.WriteFile(script, []byte("# original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	for _, target := range []string{launcher, script} {
		if err := os.Link(target, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveMCPServer(ctx, "", input); err == nil {
			t.Fatalf("approved a hard-linked launch file: %s", target)
		}
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"OPENAI_API_KEY", "CODEX_API_KEY"} {
		input.EnvVars = []string{name}
		if _, err := store.SaveMCPServer(ctx, "", input); err == nil || !strings.Contains(err.Error(), "reserves") {
			t.Fatalf("accepted reserved variable %s: %v", name, err)
		}
	}
	input.EnvVars = []string{"MY_MCP_API_KEY"}
	server, err := store.SaveMCPServer(ctx, "", input)
	if err != nil || server.NeedsReview {
		t.Fatalf("save: %+v, %v", server, err)
	}
	runtime := &CodexRuntime{MCPStore: store}
	realScript, err := filepath.EvalSymlinks(script)
	if err != nil {
		t.Fatal(err)
	}
	if active, roots, err := runtime.mcpForRun(ctx, Turn{}); err != nil || len(active) != 1 || !slices.Contains(roots, realScript) {
		t.Fatalf("verified launch: %+v, %v, %v", active, roots, err)
	}
	if err := os.Link(script, alias); err != nil {
		t.Fatal(err)
	}
	if active, _, err := runtime.mcpForRun(ctx, Turn{}); err != nil || len(active) != 0 {
		t.Fatalf("a new hard link must block startup: %+v, %v", active, err)
	}
	config, err := store.MCPServerConfig(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.TestMCPServer(ctx, config); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("Test must also reject a new hard link: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("# replaced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if active, _, err := runtime.mcpForRun(ctx, Turn{}); err != nil || len(active) != 0 {
		t.Fatalf("changed contents must block startup: %+v, %v", active, err)
	}
	if _, err := store.SaveMCPServer(ctx, server.ID, input); err != nil {
		t.Fatal(err)
	}
	if active, _, err := runtime.mcpForRun(ctx, Turn{}); err != nil || len(active) != 1 {
		t.Fatalf("reapproval must restore startup: %+v, %v", active, err)
	}
}

func TestMCPMigrationDoesNotApproveOldLocalCommands(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	install := t.TempDir()
	launcher := filepath.Join(install, "server")
	script := filepath.Join(install, "late.py")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// An older Buddy could store a launch path before the file appeared.
	_, err = db.ExecContext(ctx, `
		CREATE TABLE mcp_servers(id TEXT PRIMARY KEY,name TEXT UNIQUE,transport TEXT,url TEXT,command TEXT,
			args TEXT,env_vars TEXT,tools TEXT,enabled INTEGER,token TEXT);
		INSERT INTO mcp_servers VALUES('local','local','stdio','',?,json_array(?),'[]','[]',1,'');
		INSERT INTO mcp_servers VALUES('remote','remote','http','https://mcp.example/mcp','','[]','[]','[]',1,'token');
	`, launcher, script)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	local, err := store.MCPServerConfig(ctx, "local")
	if err != nil || !local.NeedsReview || local.ApproveTools {
		t.Fatalf("unsafe legacy approval: %+v, %v", local, err)
	}
	if err := os.WriteFile(script, []byte("# appeared after upgrade\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := &CodexRuntime{MCPStore: store}
	active, _, err := runtime.mcpForRun(ctx, Turn{})
	if err != nil || len(active) != 1 || active[0].ID != "remote" || !active[0].HasToken {
		t.Fatalf("upgrade must preserve remote configuration only: %d active, %v", len(active), err)
	}
}
