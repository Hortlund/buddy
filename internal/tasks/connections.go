package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const connectionSchema = `
CREATE TABLE IF NOT EXISTS provider_connections (
	id TEXT PRIMARY KEY, source TEXT NOT NULL, name TEXT NOT NULL,
	account TEXT NOT NULL, endpoint TEXT NOT NULL, identity TEXT NOT NULL, secret TEXT NOT NULL,
	client_id TEXT NOT NULL, client_secret TEXT NOT NULL,
	connected INTEGER NOT NULL, checked INTEGER NOT NULL, problem TEXT NOT NULL,
	UNIQUE(source,name,identity,endpoint)
);
CREATE TABLE IF NOT EXISTS gmail_oauth_client (
	id INTEGER PRIMARY KEY CHECK (id=1), client_id TEXT NOT NULL, client_secret TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS github_collaboration (
	connection_id TEXT PRIMARY KEY REFERENCES provider_connections(id), revision TEXT NOT NULL
);
`

type ProviderConnection struct {
	ID            string    `json:"id"`
	Source        string    `json:"source"`
	Name          string    `json:"name"`
	Account       string    `json:"account,omitempty"`
	Endpoint      string    `json:"endpoint,omitempty"`
	Connected     bool      `json:"connected"`
	CheckedAt     time.Time `json:"checked_at"`
	Problem       string    `json:"problem,omitempty"`
	Collaboration bool      `json:"collaboration,omitempty"`
}

// ProviderCredentials stays on the server. Owner responses only return the
// public ProviderConnection fields.
type ProviderCredentials struct {
	ProviderConnection
	Identity      string `json:"-"`
	Secret        string `json:"-"`
	ClientID      string `json:"-"`
	ClientSecret  string `json:"-"`
	WriteRevision string `json:"-"`
}

const providerColumns = `id,source,name,account,endpoint,connected,checked,problem,
	EXISTS(SELECT 1 FROM github_collaboration WHERE connection_id=provider_connections.id)`

func providerFields(c *ProviderConnection, checked *int64) []any {
	return []any{&c.ID, &c.Source, &c.Name, &c.Account, &c.Endpoint, &c.Connected, checked, &c.Problem, &c.Collaboration}
}

func (s *Store) ProviderConnections(ctx context.Context) ([]ProviderConnection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+providerColumns+" FROM provider_connections ORDER BY source,name COLLATE NOCASE,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderConnection
	for rows.Next() {
		var c ProviderConnection
		var checked int64
		if err := rows.Scan(providerFields(&c, &checked)...); err != nil {
			return nil, err
		}
		c.CheckedAt = time.UnixMicro(checked).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ProviderCredential(ctx context.Context, id string) (ProviderCredentials, error) {
	var c ProviderCredentials
	var checked int64
	fields := append(providerFields(&c.ProviderConnection, &checked), &c.Identity, &c.Secret, &c.ClientID, &c.ClientSecret, &c.WriteRevision)
	err := s.db.QueryRowContext(ctx, "SELECT "+providerColumns+`,identity,secret,client_id,client_secret,
		COALESCE((SELECT revision FROM github_collaboration WHERE connection_id=provider_connections.id),'')
		FROM provider_connections WHERE id=?`, id).Scan(fields...)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	c.CheckedAt = time.UnixMicro(checked).UTC()
	return c, err
}

func (s *Store) SaveProviderConnection(ctx context.Context, c ProviderCredentials) (ProviderConnection, error) {
	c.Name = strings.TrimSpace(c.Name)
	if !validTitle(c.Name, 100) {
		return ProviderConnection{}, &InputError{"Give this connection a name under 100 bytes."}
	}
	c.Connected, c.CheckedAt, c.Problem = true, time.Now().UTC().Truncate(time.Microsecond), ""
	c.Collaboration = c.Collaboration && c.Source == GitHubCI
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderConnection{}, err
	}
	defer tx.Rollback()
	if c.ID == "" {
		c.ID = "con_" + rand.Text()
		result, err := tx.ExecContext(ctx, `INSERT INTO provider_connections
			(id,source,name,account,endpoint,identity,secret,client_id,client_secret,connected,checked,problem)
			SELECT ?,?,?,?,?,?,?,?,?,1,?,'' WHERE (SELECT count(*) FROM provider_connections)<100
			ON CONFLICT(source,name,identity,endpoint) DO NOTHING`,
			c.ID, c.Source, c.Name, c.Account, c.Endpoint, c.Identity, c.Secret, c.ClientID, c.ClientSecret, c.CheckedAt.UnixMicro())
		if err := requireStandingChange(result, err); err != nil {
			if errors.Is(err, ErrInactive) {
				return ProviderConnection{}, &InputError{"An identical connection already exists, or this Buddy server has reached its connection limit. Check the saved connections."}
			}
			return ProviderConnection{}, err
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE provider_connections SET name=?,account=?,secret=?,client_id=?,client_secret=?,
			connected=1,checked=?,problem='' WHERE id=? AND source=? AND identity=? AND endpoint=?
			AND NOT EXISTS(SELECT 1 FROM provider_connections other WHERE other.id<>provider_connections.id
				AND other.source=provider_connections.source AND other.identity=provider_connections.identity
				AND other.endpoint=provider_connections.endpoint AND other.name=?)`,
			c.Name, c.Account, c.Secret, c.ClientID, c.ClientSecret, c.CheckedAt.UnixMicro(), c.ID, c.Source, c.Identity, c.Endpoint, c.Name)
		if err := requireStandingChange(result, err); err != nil {
			if errors.Is(err, ErrInactive) {
				return ProviderConnection{}, &InputError{"Reconnect the same account or instance and use a distinct connection name."}
			}
			return ProviderConnection{}, err
		}
	}
	if c.Collaboration {
		_, err = tx.ExecContext(ctx, `INSERT INTO github_collaboration VALUES (?,?)
			ON CONFLICT(connection_id) DO UPDATE SET revision=excluded.revision`, c.ID, rand.Text())
	} else {
		_, err = tx.ExecContext(ctx, "DELETE FROM github_collaboration WHERE connection_id=?", c.ID)
	}
	if err != nil {
		return ProviderConnection{}, err
	}
	return c.ProviderConnection, tx.Commit()
}

func (s *Store) NoteProviderCheck(ctx context.Context, c ProviderCredentials, problem string) (ProviderConnection, error) {
	c.CheckedAt, c.Problem = time.Now().UTC().Truncate(time.Microsecond), problem
	result, err := s.db.ExecContext(ctx, `UPDATE provider_connections SET checked=?,problem=?
		WHERE id=? AND connected=1 AND secret=? AND client_id=? AND client_secret=?
		AND COALESCE((SELECT revision FROM github_collaboration WHERE connection_id=provider_connections.id),'')=?`,
		c.CheckedAt.UnixMicro(), problem, c.ID, c.Secret, c.ClientID, c.ClientSecret, c.WriteRevision)
	if err := requireStandingChange(result, err); err != nil {
		return ProviderConnection{}, err
	}
	return c.ProviderConnection, nil
}

// DisconnectProvider stops new signals and pending approvals in the same
// transaction that removes the credential. Already accepted jobs keep their evidence.
func (s *Store) DisconnectProvider(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var source string
	if err := tx.QueryRowContext(ctx, "SELECT source FROM provider_connections WHERE id=?", id).Scan(&source); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_connections
		SET connected=0,secret='',client_id='',client_secret='',problem='' WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM github_collaboration WHERE connection_id=?", id); err != nil {
		return err
	}
	key := WatchSourceKey(source, id)
	if _, err := tx.ExecContext(ctx, "UPDATE standing_watches SET enabled=0 WHERE source=? AND enabled=1", key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE standing_inbox SET state='dismissed',seen=1 WHERE state IN ('pending','approval')
		AND watch_id IN (SELECT id FROM standing_watches WHERE source=?)`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GmailOAuthClient(ctx context.Context) (id, secret string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT client_id,client_secret FROM gmail_oauth_client
		UNION ALL SELECT client_id,client_secret FROM provider_connections
		WHERE source=? AND connected=1 AND client_id<>'' LIMIT 1`, GmailLabel).Scan(&id, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func (s *Store) SaveGmailOAuthClient(ctx context.Context, id, secret string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO gmail_oauth_client VALUES (1,?,?)
		ON CONFLICT(id) DO UPDATE SET client_id=excluded.client_id,client_secret=excluded.client_secret`, id, secret)
	return err
}

func WatchSourceKey(source, connection string) string {
	if connection == "" {
		return source
	}
	return source + ":" + connection
}
