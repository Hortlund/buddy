package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const mobileSchema = `
CREATE TABLE IF NOT EXISTS mobile_pairings (
	code BLOB PRIMARY KEY,
	expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mobile_clients (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	secret BLOB NOT NULL UNIQUE,
	paired INTEGER NOT NULL,
	push_token TEXT UNIQUE,
	notify_work INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS mobile_delivery (
	id TEXT PRIMARY KEY,
	client TEXT NOT NULL REFERENCES mobile_clients(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	conversation TEXT NOT NULL,
	item TEXT NOT NULL,
	due INTEGER NOT NULL,
	attempt INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS mobile_due ON mobile_delivery(due);
CREATE TRIGGER IF NOT EXISTS mobile_watch_notice AFTER INSERT ON standing_inbox
WHEN NEW.state IN ('approval','notified','overflow')
BEGIN
	INSERT INTO mobile_delivery(id,client,kind,conversation,item,due,created)
	SELECT lower(hex(randomblob(16))), id, NEW.state, NEW.conversation, NEW.id, NEW.created, NEW.created
	FROM mobile_clients WHERE push_token IS NOT NULL;
END;
DROP TRIGGER IF EXISTS mobile_work_notice;
CREATE TRIGGER IF NOT EXISTS mobile_work_terminal_notice AFTER UPDATE OF status ON tasks
WHEN NEW.kind='background_work' AND NEW.status<>OLD.status AND NEW.status IN ('succeeded','failed')
BEGIN
	INSERT INTO mobile_delivery(id,client,kind,conversation,item,due,created)
	SELECT lower(hex(randomblob(16))), m.id, NEW.status, ct.conversation_id, NEW.id, t.now, t.now
	FROM mobile_clients m JOIN chat_turns ct ON ct.task_id=NEW.id
	CROSS JOIN (SELECT CAST(unixepoch('subsec')*1000000 AS INTEGER) AS now) t
	WHERE m.push_token IS NOT NULL AND m.notify_work=1;
END;
CREATE TRIGGER IF NOT EXISTS mobile_work_question_notice AFTER INSERT ON work_inputs
WHEN NEW.answer_key IS NULL
BEGIN
	INSERT INTO mobile_delivery(id,client,kind,conversation,item,due,created)
	SELECT lower(hex(randomblob(16))), m.id, 'needs_input', ct.conversation_id, NEW.task_id, t.now, t.now
	FROM mobile_clients m JOIN chat_turns ct ON ct.task_id=NEW.job_id
	CROSS JOIN (SELECT CAST(unixepoch('subsec')*1000000 AS INTEGER) AS now) t
	WHERE m.push_token IS NOT NULL;
END;
-- Older versions stored a job ID for input alerts. Keep the newest alert
-- for each still-open question and discard any remaining job-level rows.
WITH legacy AS MATERIALIZED (
	SELECT max(d.rowid) AS rowid, q.task_id AS question
	FROM mobile_delivery d JOIN work_inputs q ON q.job_id=d.item AND q.answer_key IS NULL
	WHERE d.kind='needs_input' AND NOT EXISTS (
		SELECT 1 FROM mobile_delivery current WHERE current.client=d.client
		AND current.kind=d.kind AND current.item=q.task_id)
	GROUP BY d.client,d.item
)
UPDATE mobile_delivery AS d SET item=legacy.question FROM legacy WHERE d.rowid=legacy.rowid;
DELETE FROM mobile_delivery AS d WHERE kind='needs_input' AND NOT EXISTS (
	SELECT 1 FROM work_inputs q WHERE q.task_id=d.item);
`

var ErrMobileRevoked = errors.New("This iPhone is no longer paired.")

type mobileClientContextKey struct{}

// WithMobileClient carries the authenticated client into a write transaction.
func WithMobileClient(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, mobileClientContextKey{}, id)
}

func MobileClientFromContext(ctx context.Context) string {
	id, _ := ctx.Value(mobileClientContextKey{}).(string)
	return id
}

// Recheck access after reading the request and inside the transaction that
// commits its effects. Buddy serializes database writes, including revocation.
func (s *Store) beginMobileWrite(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if id := MobileClientFromContext(ctx); id != "" {
		var active bool
		err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM mobile_clients WHERE id=?)", id).Scan(&active)
		if err != nil || !active {
			tx.Rollback()
			if err != nil {
				return nil, err
			}
			return nil, ErrMobileRevoked
		}
	}
	return tx, nil
}

type MobilePairing struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type MobileClient struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	PairedAt      time.Time `json:"paired_at"`
	Notifications bool      `json:"notifications"`
}

type MobileCredential struct {
	MobileClient
	Token string `json:"token"`
}

func (s *Store) CreateMobilePairing(ctx context.Context) (MobilePairing, error) {
	now := time.Now().UTC()
	pair := MobilePairing{Code: rand.Text(), ExpiresAt: now.Add(5 * time.Minute)}
	digest := sha256.Sum256([]byte(pair.Code))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MobilePairing{}, err
	}
	defer tx.Rollback()
	// Only the most recently displayed QR code can be redeemed.
	if _, err := tx.ExecContext(ctx, "DELETE FROM mobile_pairings"); err != nil {
		return MobilePairing{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO mobile_pairings VALUES(?,?)", digest[:], pair.ExpiresAt.UnixMicro()); err != nil {
		return MobilePairing{}, err
	}
	return pair, tx.Commit()
}

func (s *Store) PairMobileClient(ctx context.Context, code, name string) (MobileCredential, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || strings.ContainsFunc(name, unicode.IsControl) {
		return MobileCredential{}, &InputError{"Enter a device name up to 100 bytes."}
	}
	if len(code) != len(rand.Text()) {
		return MobileCredential{}, ErrNotFound
	}
	now := time.Now().UTC()
	credential := MobileCredential{
		MobileClient: MobileClient{ID: "ios_" + rand.Text(), Name: name, PairedAt: now},
		Token:        "mobile_" + rand.Text() + rand.Text(),
	}
	invite := sha256.Sum256([]byte(code))
	secret := sha256.Sum256([]byte(credential.Token))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MobileCredential{}, err
	}
	defer tx.Rollback()
	used, err := tx.ExecContext(ctx, "DELETE FROM mobile_pairings WHERE code=? AND expires>?", invite[:], now.UnixMicro())
	if err != nil {
		return MobileCredential{}, err
	}
	if n, err := used.RowsAffected(); err != nil {
		return MobileCredential{}, err
	} else if n != 1 {
		return MobileCredential{}, ErrNotFound
	}
	added, err := tx.ExecContext(ctx, `INSERT INTO mobile_clients(id,name,secret,paired)
		SELECT ?,?,?,? WHERE (SELECT count(*) FROM mobile_clients)<32`,
		credential.ID, name, secret[:], now.UnixMicro())
	if err != nil {
		return MobileCredential{}, err
	}
	if n, err := added.RowsAffected(); err != nil {
		return MobileCredential{}, err
	} else if n != 1 {
		return MobileCredential{}, &InputError{"Remove an unused iPhone in Connections before pairing another."}
	}
	return credential, tx.Commit()
}

func (s *Store) MobileClientID(ctx context.Context, token string) (string, error) {
	if !strings.HasPrefix(token, "mobile_") || len(token) > 128 {
		return "", ErrNotFound
	}
	secret := sha256.Sum256([]byte(token))
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM mobile_clients WHERE secret=?", secret[:]).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func (s *Store) MobileClients(ctx context.Context) ([]MobileClient, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,paired,push_token IS NOT NULL FROM mobile_clients ORDER BY paired DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := make([]MobileClient, 0)
	for rows.Next() {
		var client MobileClient
		var paired int64
		if err := rows.Scan(&client.ID, &client.Name, &paired, &client.Notifications); err != nil {
			return nil, err
		}
		client.PairedAt = time.UnixMicro(paired).UTC()
		clients = append(clients, client)
	}
	return clients, rows.Err()
}

func (s *Store) RevokeMobileClient(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM mobile_clients WHERE id=?", id)
	return err
}

var mobilePushToken = regexp.MustCompile(`^[0-9a-f]{2,512}$`)

func (s *Store) SetMobileNotifications(ctx context.Context, id, token string, work bool) error {
	if token != "" && (!mobilePushToken.MatchString(token) || len(token)%2 != 0) {
		return &InputError{"Invalid iPhone notification token."}
	}
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if token != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE mobile_clients SET push_token=NULL,notify_work=0 WHERE push_token=? AND id<>?", token, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE mobile_clients SET push_token=NULLIF(?,''),notify_work=? WHERE id=?", token, work, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mobile_delivery WHERE client IN
		(SELECT id FROM mobile_clients WHERE push_token IS NULL)
		OR (client=? AND ?=0 AND kind IN ('succeeded','failed'))`, id, work); err != nil {
		return err
	}
	return tx.Commit()
}

type MobileDelivery struct {
	ID, Client, Token, Kind, Conversation, Item string
	Attempt                                     int
	CreatedAt                                   time.Time
}

func (s *Store) PruneMobileDeliveries(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mobile_delivery AS d
		WHERE created<? OR
			(kind IN ('approval','notified','overflow') AND NOT EXISTS
				(SELECT 1 FROM standing_inbox i WHERE i.id=d.item AND i.state=d.kind AND i.seen=0))
			OR (kind='needs_input' AND NOT EXISTS (
				SELECT 1 FROM work_inputs q JOIN tasks t ON t.id=q.job_id
				WHERE q.task_id=d.item AND q.answer_key IS NULL AND t.status='needs_input'))
			OR (kind IN ('succeeded','failed') AND NOT EXISTS
				(SELECT 1 FROM tasks t WHERE t.id=d.item AND t.status=d.kind))`, now.Add(-24*time.Hour).UnixMicro())
	return err
}

func (s *Store) NextMobileDelivery(ctx context.Context, now time.Time) (MobileDelivery, error) {
	if err := s.PruneMobileDeliveries(ctx, now); err != nil {
		return MobileDelivery{}, err
	}
	var n MobileDelivery
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT d.id,d.client,m.push_token,d.kind,d.conversation,COALESCE(q.job_id,d.item),d.attempt,d.created
		FROM mobile_delivery d JOIN mobile_clients m ON m.id=d.client
		LEFT JOIN work_inputs q ON d.kind='needs_input' AND q.task_id=d.item AND q.answer_key IS NULL
		WHERE d.due<=? AND m.push_token IS NOT NULL AND (d.kind<>'needs_input' OR q.task_id IS NOT NULL)
		ORDER BY d.due,d.id LIMIT 1`, now.UnixMicro()).
		Scan(&n.ID, &n.Client, &n.Token, &n.Kind, &n.Conversation, &n.Item, &n.Attempt, &created)
	n.CreatedAt = time.UnixMicro(created)
	return n, err
}

func (s *Store) CompleteMobileDelivery(ctx context.Context, n MobileDelivery, retry bool, now time.Time) error {
	if retry && n.Attempt < 5 {
		due := now.Add(time.Minute << n.Attempt)
		_, err := s.db.ExecContext(ctx, "UPDATE mobile_delivery SET due=?,attempt=attempt+1 WHERE id=?", due.UnixMicro(), n.ID)
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM mobile_delivery WHERE id=?", n.ID)
	return err
}

func (s *Store) InvalidateMobileNotifications(ctx context.Context, n MobileDelivery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE mobile_clients SET push_token=NULL,notify_work=0 WHERE id=? AND push_token=?", n.Client, n.Token); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mobile_delivery WHERE client=? AND EXISTS
		(SELECT 1 FROM mobile_clients WHERE id=? AND push_token IS NULL)`, n.Client, n.Client); err != nil {
		return err
	}
	return tx.Commit()
}
