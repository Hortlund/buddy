package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"buddy/internal/localconfig"
)

const mcpSchema = `
CREATE TABLE IF NOT EXISTS mcp_servers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL COLLATE NOCASE UNIQUE,
	transport TEXT NOT NULL CHECK(transport IN ('http','stdio')),
	url TEXT NOT NULL,
	command TEXT NOT NULL,
	args TEXT NOT NULL,
	env_vars TEXT NOT NULL,
	tools TEXT NOT NULL,
	token TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	approve_tools INTEGER NOT NULL DEFAULT 0,
	launch_fingerprint TEXT NOT NULL DEFAULT ''
);
`

const (
	MCPHTTP  = "http"
	MCPStdio = "stdio"
)

type MCPServer struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	URL          string   `json:"url,omitempty"`
	Command      string   `json:"command,omitempty"`
	Args         []string `json:"args,omitempty"`
	EnvVars      []string `json:"env_vars,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Enabled      bool     `json:"enabled"`
	HasToken     bool     `json:"has_token"`
	ApproveTools bool     `json:"approve_tools,omitempty"`
	NeedsReview  bool     `json:"needs_review,omitempty"`
}

type MCPServerSetup struct {
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	URL          string   `json:"url,omitempty"`
	Command      string   `json:"command,omitempty"`
	Args         []string `json:"args,omitempty"`
	EnvVars      []string `json:"env_vars,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	ApproveTools bool     `json:"approve_tools,omitempty"`
	// Omit to keep the token for the same URL. An empty string clears it.
	Token *string `json:"token,omitempty"`
}

type MCPServerConfig struct {
	MCPServer
	token, launchFingerprint string
}

const mcpColumns = "id,name,transport,url,command,args,env_vars,tools,enabled,token,approve_tools,launch_fingerprint"

var mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`)
var mcpEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func reservedMCPName(name string) bool {
	switch strings.ToLower(name) {
	case "buddy-work", "buddy-cua", "buddy-playwright":
		return true
	}
	return false
}

func validMCPTool(name string) bool {
	return name != "" && len(name) <= 128 && !strings.ContainsFunc(name, unicode.IsSpace) && !strings.ContainsFunc(name, unicode.IsControl)
}

func (input *MCPServerSetup) validate() error {
	input.Name = strings.TrimSpace(input.Name)
	if !mcpName.MatchString(input.Name) {
		return &InputError{"Use up to 48 letters, numbers, hyphens, or underscores for the server name."}
	}
	if reservedMCPName(input.Name) {
		return &InputError{"Buddy reserves this server name for built-in tools. Choose another name."}
	}
	if len(input.Tools) > 100 || len(input.Args) > 32 || len(input.EnvVars) > 32 {
		return &InputError{"Too many tool names, arguments, or environment variables."}
	}
	if input.ApproveTools && len(input.Tools) == 0 {
		return &InputError{"List the exact tools you want to allow before preapproving MCP tools."}
	}
	seenTools := make(map[string]bool, len(input.Tools))
	for _, tool := range input.Tools {
		if !validMCPTool(tool) {
			return &InputError{"Enter one exact tool name per line."}
		}
		if seenTools[tool] {
			return &InputError{"List each MCP tool only once."}
		}
		seenTools[tool] = true
	}
	switch input.Transport {
	case MCPHTTP:
		input.URL = strings.TrimSpace(input.URL)
		u, err := url.Parse(input.URL)
		if err != nil || len(input.URL) > 2048 || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
			(u.Scheme != "https" && !(u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || localconfig.IsLoopback(u.Hostname())))) {
			return &InputError{"Enter an HTTPS MCP URL, or an HTTP URL on the Buddy server's loopback address."}
		}
		if input.Token != nil {
			token := strings.TrimSpace(*input.Token)
			if len(token) > 4096 || strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r >= 127 }) {
				return &InputError{"Enter a bearer token without spaces or control characters."}
			}
			input.Token = &token
		}
		input.Command, input.Args, input.EnvVars = "", nil, nil
	case MCPStdio:
		input.Command = strings.TrimSpace(input.Command)
		if input.Command == "" || len(input.Command) > 1024 || strings.ContainsFunc(input.Command, unicode.IsControl) ||
			(!filepath.IsAbs(input.Command) && (strings.ContainsAny(input.Command, `/\`) || strings.ContainsFunc(input.Command, unicode.IsSpace))) {
			return &InputError{"Enter an executable name or absolute path. Put its arguments in the separate field."}
		}
		for _, arg := range input.Args {
			if len(arg) > 2048 || strings.ContainsFunc(arg, unicode.IsControl) {
				return &InputError{"Use one argument per line, without control characters."}
			}
		}
		for _, name := range input.EnvVars {
			if !mcpEnvName.MatchString(name) {
				return &InputError{"Enter environment variable names such as API_TOKEN, one per line."}
			}
			if codexCredentialEnv(name) {
				return &InputError{"Buddy reserves OPENAI_API_KEY and CODEX_API_KEY for Codex. Use a dedicated variable for this MCP server."}
			}
		}
		input.URL, input.Token = "", nil
	default:
		return &InputError{"Choose Streamable HTTP or a local command."}
	}
	return nil
}

func scanMCPServer(row scanner) (MCPServerConfig, error) {
	var c MCPServerConfig
	var args, env, tools string
	if err := row.Scan(&c.ID, &c.Name, &c.Transport, &c.URL, &c.Command, &args, &env, &tools, &c.Enabled, &c.token, &c.ApproveTools, &c.launchFingerprint); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	if err := errors.Join(json.Unmarshal([]byte(args), &c.Args), json.Unmarshal([]byte(env), &c.EnvVars),
		json.Unmarshal([]byte(tools), &c.Tools)); err != nil {
		return c, errors.New("Invalid saved MCP configuration.")
	}
	c.HasToken = c.token != ""
	c.NeedsReview = reservedMCPName(c.Name) || (c.Transport == MCPStdio && c.launchFingerprint == "")
	return c, nil
}

func (s *Store) mcpServers(ctx context.Context) ([]MCPServerConfig, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+mcpColumns+" FROM mcp_servers ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var servers []MCPServerConfig
	for rows.Next() {
		server, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

func (s *Store) MCPServers(ctx context.Context) ([]MCPServer, error) {
	servers, err := s.mcpServers(ctx)
	out := make([]MCPServer, 0, len(servers))
	for _, server := range servers {
		out = append(out, server.MCPServer)
	}
	return out, err
}

func (s *Store) MCPServerConfig(ctx context.Context, id string) (MCPServerConfig, error) {
	return scanMCPServer(s.db.QueryRowContext(ctx, "SELECT "+mcpColumns+" FROM mcp_servers WHERE id=?", id))
}

func (s *Store) SaveMCPServer(ctx context.Context, id string, input MCPServerSetup) (MCPServer, error) {
	if err := input.validate(); err != nil {
		return MCPServer{}, err
	}
	var fingerprint string
	if input.Transport == MCPStdio {
		launch, err := inspectMCPLaunch(ctx, input.Command, input.Args)
		if err != nil {
			return MCPServer{}, err
		}
		fingerprint = launch.fingerprint
	}
	args, _ := json.Marshal(input.Args)
	env, _ := json.Marshal(input.EnvVars)
	tools, _ := json.Marshal(input.Tools)
	var row *sql.Row
	if id == "" {
		row = s.db.QueryRowContext(ctx, `INSERT INTO mcp_servers (`+mcpColumns+`)
			SELECT ?,?,?,?,?,?,?,?,1,coalesce(?, ''),?,? WHERE (SELECT count(*) FROM mcp_servers)<24
			ON CONFLICT(name) DO NOTHING RETURNING `+mcpColumns,
			"mcp_"+rand.Text(), input.Name, input.Transport, input.URL, input.Command, string(args), string(env), string(tools), input.Token, input.ApproveTools, fingerprint)
	} else {
		row = s.db.QueryRowContext(ctx, `UPDATE mcp_servers SET name=?,transport=?,url=?,command=?,args=?,env_vars=?,tools=?,approve_tools=?,launch_fingerprint=?,
			token=CASE WHEN ?<>'http' THEN '' WHEN ? IS NOT NULL THEN ? WHEN transport='http' AND url=? THEN token ELSE '' END
			WHERE id=? AND NOT EXISTS (SELECT 1 FROM mcp_servers other WHERE other.id<>? AND other.name=?)
			RETURNING `+mcpColumns,
			input.Name, input.Transport, input.URL, input.Command, string(args), string(env), string(tools), input.ApproveTools, fingerprint,
			input.Transport, input.Token, input.Token, input.URL, id, id, input.Name)
	}
	server, err := scanMCPServer(row)
	if errors.Is(err, ErrNotFound) {
		if id != "" {
			if _, err := s.MCPServerConfig(ctx, id); err != nil {
				return MCPServer{}, err
			}
		}
		return MCPServer{}, &InputError{"Use a unique name. Buddy can store up to 24 MCP servers."}
	}
	return server.MCPServer, err
}

func migrateMCPServers(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var upgraded bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('mcp_servers') WHERE name='launch_fingerprint')`).Scan(&upgraded); err != nil {
		return err
	}
	if !upgraded {
		if _, err := tx.ExecContext(ctx, `
			ALTER TABLE mcp_servers ADD COLUMN approve_tools INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE mcp_servers ADD COLUMN launch_fingerprint TEXT NOT NULL DEFAULT '';
		`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetMCPServerEnabled(ctx context.Context, id string, enabled bool) (MCPServer, error) {
	if enabled {
		server, err := s.MCPServerConfig(ctx, id)
		if err != nil {
			return MCPServer{}, err
		}
		if reservedMCPName(server.Name) {
			return MCPServer{}, &InputError{"Rename this MCP server before enabling it. Buddy reserves its current name for built-in tools."}
		}
	}
	server, err := scanMCPServer(s.db.QueryRowContext(ctx, "UPDATE mcp_servers SET enabled=? WHERE id=? RETURNING "+mcpColumns, enabled, id))
	return server.MCPServer, err
}

func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM mcp_servers WHERE id=?", id)
	return err
}
