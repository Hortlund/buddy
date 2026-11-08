package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func mcpStringArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, tomlString(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func writeMCPConfig(home string, servers []MCPServerConfig) error {
	if len(servers) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(servers))
	for _, server := range servers {
		if seen[server.Name] {
			return &InputError{"MCP server names must be unique."}
		}
		seen[server.Name] = true
	}
	var config strings.Builder
	config.WriteString("mcp_oauth_credentials_store = \"file\"\n")
	for _, server := range servers {
		section := "mcp_servers." + tomlString(server.Name)
		config.WriteString("\n[" + section + "]\ndefault_tools_approval_mode = \"writes\"\n")
		if server.Transport == MCPStdio {
			cwd := filepath.Join(home, "mcp-work", server.Name)
			if err := os.MkdirAll(cwd, 0700); err != nil {
				return err
			}
			config.WriteString("command = " + tomlString(server.Command) + "\ncwd = " + tomlString(cwd) + "\n")
			config.WriteString("args = " + mcpStringArray(server.Args) + "\n")
			config.WriteString("env_vars = " + mcpStringArray(server.EnvVars) + "\n")
		} else {
			config.WriteString("url = " + tomlString(server.URL) + "\n")
			if server.token != "" {
				config.WriteString("http_headers = { Authorization = " + tomlString("Bearer "+server.token) + " }\n")
			}
		}
		if len(server.Tools) != 0 {
			config.WriteString("enabled_tools = " + mcpStringArray(server.Tools) + "\n")
		}
		if server.ApproveTools {
			for _, tool := range server.Tools {
				config.WriteString("[" + section + ".tools." + tomlString(tool) + "]\napproval_mode = \"approve\"\n")
			}
		}
	}
	// The isolated home is denied to model tools. Keep credentials out of
	// arguments, the shell environment, and the personal Codex config.
	return os.WriteFile(filepath.Join(home, "config.toml"), []byte(config.String()), 0600)
}

// mcpForRun takes one configuration snapshot per turn. Local installation
// paths stay read-only even while their servers are disabled.
func (r *CodexRuntime) mcpForRun(ctx context.Context, turn Turn) ([]MCPServerConfig, []string, error) {
	automation, automationRoots, err := r.executionMCP(ctx, turn)
	if err != nil {
		return nil, nil, err
	}
	if r.MCPStore == nil || turn.DeviceExecution || turn.Origin == "peer_reply" {
		return automation, automationRoots, nil
	}
	configs, err := r.MCPStore.mcpServers(ctx)
	if err != nil {
		return nil, nil, err
	}
	enabled := automation
	roots := automationRoots
	for _, config := range configs {
		if config.Transport == MCPStdio {
			protected, err := config.verifyMCPLaunch(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				continue // Another MCP server may still be available after a host move.
			}
			for _, path := range protected {
				if !slices.Contains(roots, path) {
					roots = append(roots, path)
				}
			}
		}
		if config.Enabled && !reservedMCPName(config.Name) && turn.WatchSignal == nil {
			enabled = append(enabled, config)
		}
	}
	return enabled, roots, nil
}

type MCPTestResult struct {
	Tools            []string `json:"tools"`
	RequiresApproval []string `json:"requires_approval,omitempty"`
}

func (r *CodexRuntime) TestMCPServer(ctx context.Context, server MCPServerConfig) (MCPTestResult, error) {
	var result MCPTestResult
	if server.ID != "" && reservedMCPName(server.Name) {
		return result, &InputError{"Rename this MCP server before testing it. Buddy reserves its current name for built-in tools."}
	}
	if r.lookupErr != nil {
		return result, &InputError{"Install the Codex CLI on the Buddy server to test this connection."}
	}
	if err := validateProtectedRoots(r.readOnly, true, r.attachmentDir); err != nil {
		return result, errors.New("Buddy couldn't verify its runner installation.")
	}
	if server.Transport == MCPStdio {
		if _, err := server.verifyMCPLaunch(ctx); err != nil {
			return result, err
		}
	}
	home, err := os.MkdirTemp("", "buddy-mcp-test-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(home)
	if err := writeMCPConfig(home, []MCPServerConfig{server}); err != nil {
		return result, err
	}
	// An explicit Test can wait for the full startup timeout. Ordinary turns
	// use Codex's short shared grace period for optional tools.
	flags := []string{"-c", "mcp_optional_startup_grace_ms=0"}
	err = withAppSession(ctx, r.path, home, home, "", flags, false, nil, func(s *appSession) error {
		var page struct {
			Data []struct {
				Name  string `json:"name"`
				Tools map[string]struct {
					Annotations struct {
						ReadOnlyHint bool `json:"readOnlyHint"`
					} `json:"annotations"`
				} `json:"tools"`
				Capabilities json.RawMessage `json:"serverCapabilities"`
			} `json:"data"`
		}
		if err := s.call(ctx, "mcpServerStatus/list", map[string]any{
			"serverName": server.Name, "detail": "toolsAndAuthOnly",
		}, &page); err != nil {
			return err
		}
		for _, status := range page.Data {
			if status.Name != server.Name || ((len(status.Capabilities) == 0 || string(status.Capabilities) == "null") && len(status.Tools) == 0) {
				continue
			}
			for name, tool := range status.Tools {
				if !validMCPTool(name) {
					continue
				}
				if tool.Annotations.ReadOnlyHint || (server.ApproveTools && slices.Contains(server.Tools, name)) {
					result.Tools = append(result.Tools, name)
				} else {
					result.RequiresApproval = append(result.RequiresApproval, name)
				}
			}
			slices.Sort(result.Tools)
			slices.Sort(result.RequiresApproval)
			return nil
		}
		return errors.New("MCP handshake did not complete")
	})
	if err != nil {
		return MCPTestResult{}, &InputError{"Buddy couldn't connect. Check the address or command on the server, the credentials, and whether it uses the selected transport."}
	}
	return result, nil
}
