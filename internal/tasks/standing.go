package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"
)

const watchTable = ` (
	id TEXT PRIMARY KEY, conversation TEXT NOT NULL REFERENCES conversations(id),
	name TEXT NOT NULL, source TEXT NOT NULL, scope TEXT NOT NULL, mode TEXT NOT NULL,
	instructions TEXT NOT NULL, ignore TEXT NOT NULL, budget INTEGER NOT NULL,
	enabled INTEGER NOT NULL, created INTEGER NOT NULL, filters TEXT NOT NULL DEFAULT '[]',
	activated_at INTEGER NOT NULL, gmail_history TEXT NOT NULL DEFAULT ''
);`

const standingSchema = "CREATE TABLE IF NOT EXISTS standing_watches" + watchTable + `
CREATE TABLE IF NOT EXISTS standing_signals (
	source TEXT NOT NULL, scope TEXT NOT NULL, event_key TEXT NOT NULL,
	body TEXT NOT NULL, received INTEGER NOT NULL, PRIMARY KEY(source,scope,event_key)
);
CREATE TABLE IF NOT EXISTS standing_inbox (
	id TEXT PRIMARY KEY, watch_id TEXT NOT NULL REFERENCES standing_watches(id),
	conversation TEXT NOT NULL REFERENCES conversations(id), bud TEXT NOT NULL REFERENCES agents(id),
	signal TEXT NOT NULL, assignment TEXT NOT NULL, budget INTEGER NOT NULL, state TEXT NOT NULL,
	task_id TEXT UNIQUE REFERENCES tasks(id), attempts INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL, seen INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS standing_pending ON standing_inbox(state,created);
CREATE TABLE IF NOT EXISTS standing_memory (
	id TEXT PRIMARY KEY, bud TEXT NOT NULL REFERENCES agents(id), note TEXT NOT NULL,
	source TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS standing_poll (
	source TEXT NOT NULL, scope TEXT NOT NULL, cursor TEXT NOT NULL,
	checked INTEGER NOT NULL, success INTEGER NOT NULL, problem TEXT NOT NULL,
	PRIMARY KEY(source,scope)
);
CREATE TABLE IF NOT EXISTS standing_delivery (
	id TEXT PRIMARY KEY REFERENCES standing_inbox(id), generation TEXT NOT NULL, due INTEGER NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0, sent INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS standing_watch_overflow (
	watch_id TEXT PRIMARY KEY REFERENCES standing_watches(id),
	conversation TEXT NOT NULL REFERENCES conversations(id),
	task_id TEXT NOT NULL REFERENCES chat_turns(task_id),
	began INTEGER NOT NULL,
	count INTEGER NOT NULL
);
`

const WatchOrigin = "watch"

const (
	GitHubCI         = "github_ci"
	GmailLabel       = "gmail_label"
	GrafanaAlerts    = "grafana_alerts"
	PrometheusAlerts = "prometheus_alerts"
	WatchDigest      = "digest"
	WatchInvestigate = "investigate"
	WatchAsk         = "ask"
	WatchNotify      = "notify"
)

type Watch struct {
	ConnectionID   string        `json:"connection_id,omitempty"`
	ID             string        `json:"id,omitempty"`
	ConversationID string        `json:"conversation_id"`
	Name           string        `json:"name"`
	Source         string        `json:"source"`
	Scope          string        `json:"scope"`
	Mode           string        `json:"mode"`
	Instructions   string        `json:"instructions"`
	Ignore         string        `json:"ignore"`
	Budget         int           `json:"budget_minutes"`
	Enabled        bool          `json:"enabled"`
	CreatedAt      time.Time     `json:"created_at"`
	ActiveSince    time.Time     `json:"-"`
	GmailHistory   string        `json:"-"`
	Filters        []WatchFilter `json:"filters,omitempty"`
}

type StandingSignal struct {
	ConnectionID string    `json:"connection_id,omitempty"`
	Source       string    `json:"source"`
	Scope        string    `json:"scope"`
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	Detail       string    `json:"detail"`
	URL          string    `json:"url,omitempty"`
	Enriched     bool      `json:"enriched,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	Workflow     string    `json:"workflow,omitempty"`
	Sender       string    `json:"sender,omitempty"`
	Alert        string    `json:"alert,omitempty"`
	Severity     string    `json:"severity,omitempty"`
	Subject      string    `json:"-"`
	OccurredAt   time.Time `json:"-"`
	GmailHistory string    `json:"-"`
}

type InboxItem struct {
	ID             string         `json:"id"`
	Watch          string         `json:"watch"`
	ConversationID string         `json:"conversation_id"`
	Bud            string         `json:"bud"`
	Signal         StandingSignal `json:"signal"`
	State          string         `json:"state"`
	TaskID         string         `json:"task_id,omitempty"`
	InputID        string         `json:"input_id,omitempty"`
	Result         string         `json:"result,omitempty"`
	Attempts       int            `json:"attempts"`
	Seen           bool           `json:"seen"`
	At             time.Time      `json:"at"`
}

type MemoryNote struct {
	ID      string    `json:"id,omitempty"`
	AgentID string    `json:"agent_id"`
	Note    string    `json:"note"`
	Source  string    `json:"source"`
	Expires time.Time `json:"expires,omitempty"`
}

type PollState struct {
	ConnectionID string    `json:"connection_id,omitempty"`
	Source       string    `json:"source"`
	Scope        string    `json:"scope"`
	Cursor       string    `json:"-"`
	Checked      time.Time `json:"checked"`
	Success      time.Time `json:"success"`
	Problem      string    `json:"problem,omitempty"`
}

type StandingHealth struct {
	StartedAt  time.Time   `json:"started_at"`
	Queued     int         `json:"queued"`
	Running    int         `json:"running"`
	Stuck      int         `json:"stuck"`
	NeedsInput int         `json:"needs_input"`
	Overdue    int         `json:"overdue"`
	Workers    []Device    `json:"workers,omitempty"`
	Feeds      []PollState `json:"feeds,omitempty"`
	GitHub     string      `json:"github_delivery,omitempty"`
	Exhausted  int         `json:"exhausted_notifications,omitempty"`
}

var githubScope = regexp.MustCompile(`^[A-Za-z0-9-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
var gmailScope = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`)

func (s *Store) CreateWatch(ctx context.Context, watch Watch) (Watch, error) {
	watch.ID = ""
	return s.saveWatch(ctx, watch)
}

func (s *Store) UpdateWatch(ctx context.Context, id string, watch Watch) (Watch, error) {
	watch.ID = id
	return s.saveWatch(ctx, watch)
}

func (s *Store) saveWatch(ctx context.Context, watch Watch) (Watch, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Watch{}, err
	}
	defer tx.Rollback()
	watch, err = saveWatch(ctx, tx, watch)
	if err != nil {
		return Watch{}, err
	}
	return watch, tx.Commit()
}

func normalizeWatch(watch Watch) (Watch, error) {
	watch.Name, watch.Scope = strings.TrimSpace(watch.Name), strings.TrimSpace(watch.Scope)
	watch.Instructions, watch.Ignore = strings.TrimSpace(watch.Instructions), strings.TrimSpace(watch.Ignore)
	if !validTitle(watch.Name, 100) || len(watch.Instructions) > 2000 || len(watch.Ignore) > 200 || watch.Budget < 1 || watch.Budget > 30 {
		return Watch{}, &InputError{"Enter a watch name and a budget of 1–30 minutes."}
	}
	switch watch.Source {
	case GitHubCI:
		if !ValidGitHubRepo(watch.Scope) {
			return Watch{}, &InputError{"Enter the GitHub repository as owner/repo."}
		}
		watch.Scope = strings.ToLower(watch.Scope)
	case GmailLabel:
		if !gmailScope.MatchString(watch.Scope) {
			return Watch{}, &InputError{"Enter a Gmail label ID such as INBOX or Label_123."}
		}
	case GrafanaAlerts, PrometheusAlerts:
		if watch.ConnectionID == "" {
			return Watch{}, &InputError{"Choose a connected monitoring instance."}
		}
		watch.Scope = "firing"
	default:
		return Watch{}, &InputError{"Choose a supported watch source."}
	}
	switch watch.Mode {
	case WatchDigest, WatchInvestigate, WatchAsk, WatchNotify:
	default:
		return Watch{}, &InputError{"Choose Digest, Notify in chat, Investigate, or Ask first."}
	}
	filters, err := encodeWatchFilters(watch.Source, watch.Filters)
	if err != nil {
		return Watch{}, err
	}
	if err := json.Unmarshal([]byte(filters), &watch.Filters); err != nil {
		return Watch{}, err
	}
	return watch, nil
}

func saveWatch(ctx context.Context, tx *sql.Tx, watch Watch) (Watch, error) {
	watch, err := normalizeWatch(watch)
	if err != nil {
		return Watch{}, err
	}
	filterJSON, err := json.Marshal(watch.Filters)
	if err != nil {
		return Watch{}, err
	}
	filters := string(filterJSON)
	if err := requireOpenConversation(ctx, tx, watch.ConversationID); err != nil {
		return Watch{}, err
	}
	if _, err := conversationParticipant(ctx, tx, watch.ConversationID, ""); err != nil {
		return Watch{}, err
	}
	if watch.ConnectionID != "" {
		var available bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provider_connections
			WHERE id=? AND source=? AND (connected=1 OR ?<>''))`, watch.ConnectionID, watch.Source, watch.ID).Scan(&available); err != nil {
			return Watch{}, err
		}
		if !available {
			return Watch{}, &InputError{"Choose a connected account or instance for this watch."}
		}
	}
	if watch.ID != "" {
		current, err := scanWatch(tx.QueryRowContext(ctx, "SELECT "+watchColumns+" FROM standing_watches WHERE id=? AND enabled>=0", watch.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return Watch{}, ErrNotFound
		}
		if err != nil {
			return Watch{}, err
		}
		if current.Source != watch.Source || current.ConnectionID != watch.ConnectionID || current.Scope != watch.Scope {
			return Watch{}, &InputError{"Create a separate watch to use a different connection or source."}
		}
		result, err := tx.ExecContext(ctx, `UPDATE OR IGNORE standing_watches SET conversation=?,name=?,mode=?,
			instructions=?,ignore=?,budget=?,filters=? WHERE id=?`, watch.ConversationID, watch.Name, watch.Mode,
			watch.Instructions, watch.Ignore, watch.Budget, filters, watch.ID)
		if err := requireStandingChange(result, err); errors.Is(err, ErrInactive) {
			return Watch{}, &InputError{"A watch with these filters already exists for this source."}
		} else if err != nil {
			return Watch{}, err
		}
		return scanWatch(tx.QueryRowContext(ctx, "SELECT "+watchColumns+" FROM standing_watches WHERE id=?", watch.ID))
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM standing_watches WHERE enabled>=0").Scan(&count); err != nil {
		return Watch{}, err
	}
	if count >= 30 {
		return Watch{}, &InputError{"This Buddy home can have up to 30 watches."}
	}
	source := WatchSourceKey(watch.Source, watch.ConnectionID)
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM standing_watches
		WHERE source=? AND scope=? AND filters=? AND enabled>=0)`, source, watch.Scope, filters).Scan(&duplicate); err != nil {
		return Watch{}, err
	}
	if duplicate {
		return Watch{}, &InputError{"A watch with these filters already exists for this source. You can manage it in Autonomy."}
	}
	now := time.Now().UTC().UnixMicro()
	archived, err := scanWatch(tx.QueryRowContext(ctx, `UPDATE standing_watches SET conversation=?,name=?,mode=?,
		instructions=?,ignore=?,budget=?,enabled=1,activated_at=?,gmail_history=? WHERE id=(SELECT id FROM standing_watches
		WHERE source=? AND scope=? AND filters=? AND enabled=-1 ORDER BY created DESC,id LIMIT 1)
		RETURNING `+watchColumns, watch.ConversationID, watch.Name, watch.Mode, watch.Instructions, watch.Ignore, watch.Budget,
		now, watch.GmailHistory, source, watch.Scope, filters))
	if !errors.Is(err, sql.ErrNoRows) {
		return archived, err
	}
	return scanWatch(tx.QueryRowContext(ctx, `INSERT INTO standing_watches(`+watchColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		RETURNING `+watchColumns, "watch_"+rand.Text(), watch.ConversationID, watch.Name, source,
		watch.Scope, watch.Mode, watch.Instructions, watch.Ignore, watch.Budget, true, now, filters, now, watch.GmailHistory))
}

const watchColumns = "id,conversation,name,source,scope,mode,instructions,ignore,budget,enabled,created,filters,activated_at,gmail_history"

func scanWatch(row scanner) (Watch, error) {
	var w Watch
	var created, activated int64
	var filters string
	err := row.Scan(&w.ID, &w.ConversationID, &w.Name, &w.Source, &w.Scope, &w.Mode,
		&w.Instructions, &w.Ignore, &w.Budget, &w.Enabled, &created, &filters, &activated, &w.GmailHistory)
	if err != nil {
		return Watch{}, err
	}
	err = json.Unmarshal([]byte(filters), &w.Filters)
	w.Source, w.ConnectionID, _ = strings.Cut(w.Source, ":")
	w.CreatedAt = time.UnixMicro(created).UTC()
	w.ActiveSince = time.UnixMicro(activated).UTC()
	return w, err
}

func (s *Store) Watches(ctx context.Context) ([]Watch, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+watchColumns+" FROM standing_watches WHERE enabled>=0 ORDER BY created DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Watch
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) Watch(ctx context.Context, id string) (Watch, error) {
	watch, err := scanWatch(s.db.QueryRowContext(ctx, "SELECT "+watchColumns+" FROM standing_watches WHERE id=? AND enabled>=0", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Watch{}, ErrNotFound
	}
	return watch, err
}

func (s *Store) SetWatchEnabled(ctx context.Context, id string, enabled bool) error {
	return s.SetWatchEnabledWithHistory(ctx, id, enabled, "")
}

func (s *Store) SetWatchEnabledWithHistory(ctx context.Context, id string, enabled bool, gmailHistory string) error {
	state := 0
	if enabled {
		state = 1
	}
	return s.setWatchState(ctx, id, state, gmailHistory)
}

func (s *Store) ArchiveWatch(ctx context.Context, id string) error {
	return s.setWatchState(ctx, id, -1, "")
}

func (s *Store) setWatchState(ctx context.Context, id string, state int, gmailHistory string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setWatchState(ctx, tx, id, state, gmailHistory); err != nil {
		return err
	}
	return tx.Commit()
}

func setWatchState(ctx context.Context, tx *sql.Tx, id string, state int, gmailHistory string) error {
	if state == 1 {
		var chat string
		err := tx.QueryRowContext(ctx, "SELECT conversation FROM standing_watches WHERE id=? AND enabled>=0", id).Scan(&chat)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInactive
		}
		if err != nil {
			return err
		}
		if err := requireOpenConversation(ctx, tx, chat); err != nil {
			return err
		}
		var disconnected bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM standing_watches w
			WHERE w.id=? AND w.enabled>=0 AND instr(w.source,':')>0 AND NOT EXISTS(
				SELECT 1 FROM provider_connections c WHERE c.source||':'||c.id=w.source AND c.connected=1))`, id).Scan(&disconnected); err != nil {
			return err
		}
		if disconnected {
			return &InputError{"Reconnect this account or instance before resuming its watch."}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE standing_watches SET enabled=?,
		activated_at=CASE WHEN enabled=0 AND ?=1 THEN ? ELSE activated_at END,
		gmail_history=CASE WHEN enabled=0 AND ?=1 THEN ? ELSE gmail_history END WHERE id=? AND enabled>=0`,
		state, state, time.Now().UnixMicro(), state, gmailHistory, id)
	if err := requireStandingChange(result, err); err != nil {
		return err
	}
	if state != 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE standing_inbox SET state='dismissed',seen=1
			WHERE watch_id=? AND state IN ('pending','approval')`, id); err != nil {
			return err
		}
	}
	return nil
}

func requireStandingChange(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrInactive
	}
	return err
}

// RecordStandingSignal commits the fact and its disposition together. The
// provider's key must identify the logical event, including a CI run attempt.
func (s *Store) RecordStandingSignal(ctx context.Context, signal StandingSignal) (bool, error) {
	if !validTitle(signal.Key, 160) || !validTitle(signal.Title, 240) || len(signal.Detail) > 16000 || len(signal.URL) > 1024 {
		return false, &InputError{"Invalid watch event."}
	}
	if signal.URL != "" {
		link, err := url.Parse(signal.URL)
		if err != nil || link.Scheme != "https" || link.User != nil || link.Host == "" {
			return false, &InputError{"Invalid watch event link."}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	sourceKey := WatchSourceKey(signal.Source, signal.ConnectionID)
	rows, err := tx.QueryContext(ctx, "SELECT "+watchColumns+" FROM standing_watches WHERE source=? AND scope=? AND enabled=1 ORDER BY id", sourceKey, signal.Scope)
	if err != nil {
		return false, err
	}
	var watches []Watch
	for rows.Next() {
		watch, err := scanWatch(rows)
		if err != nil {
			rows.Close()
			return false, err
		}
		watches = append(watches, watch)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(watches) == 0 {
		return false, err
	}
	now := time.Now().UTC().UnixMicro()
	body, _ := json.Marshal(signal)
	recorded, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO standing_signals VALUES (?,?,?,?,?)", sourceKey, signal.Scope, signal.Key, string(body), now)
	if err != nil {
		return false, err
	}
	if n, err := recorded.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	matched := false
	for _, watch := range watches {
		if !watch.matches(signal) {
			continue
		}
		if err := deliverWatchSignal(ctx, tx, watch, signal, string(body), now); err != nil {
			return false, err
		}
		matched = true
	}
	return matched, tx.Commit()
}

func deliverWatchSignal(ctx context.Context, tx *sql.Tx, watch Watch, signal StandingSignal, body string, now int64) error {
	bud, err := conversationParticipant(ctx, tx, watch.ConversationID, "")
	if err != nil {
		return err
	}
	state := "digest"
	switch watch.Mode {
	case WatchInvestigate:
		state = "pending"
	case WatchAsk:
		state = "approval"
	case WatchNotify:
		state = "notified"
	}
	overflow := false
	if state != "digest" {
		// A burst should stay visible without monopolizing the worker.
		var scheduled int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM standing_inbox WHERE watch_id=? AND created>=?
			AND ((? AND state='notified') OR (NOT ? AND state IN ('approval','pending','queued')))`,
			watch.ID, now-int64(24*time.Hour/time.Microsecond), state == "notified", state == "notified").Scan(&scheduled); err != nil {
			return err
		}
		if scheduled >= 10 {
			overflow = state == "notified"
			state = "digest"
		}
	}
	id := "inbox_" + rand.Text()
	var taskID string
	if state == "notified" {
		card := ConnectionCard{Source: watch.Source, Connection: watch.Name, Heading: signal.Title, Body: signal.Detail,
			URL: signal.URL, WatchID: watch.ID, ConnectionID: watch.ConnectionID}
		taskID, err = postWatchNotice(ctx, tx, id, watch, bud, card)
		if err != nil {
			return err
		}
	} else if overflow {
		taskID, err = postWatchOverflow(ctx, tx, id, watch, bud, now)
		if err != nil {
			return err
		}
		if taskID != "" {
			state = "overflow"
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO standing_inbox(id,watch_id,conversation,bud,signal,assignment,budget,state,created,task_id)
		VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''))`, id, watch.ID, watch.ConversationID, bud.ID, body, watch.Instructions, watch.Budget, state, now, taskID); err != nil {
		return err
	}
	if state == "approval" {
		if _, err := tx.ExecContext(ctx, "INSERT INTO standing_delivery(id,generation,due) VALUES (?,'approval',?)", id, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) StandingSignalExists(ctx context.Context, source, scope, key string) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM standing_signals WHERE source=? AND scope=? AND event_key=?)",
		source, scope, key).Scan(&found)
	return found, err
}

func (s *Store) CheckpointStandingEvidence(ctx context.Context, signal StandingSignal) error {
	if len(signal.Detail) > 16000 {
		return &InputError{"The watch evidence is too large."}
	}
	body, _ := json.Marshal(signal)
	sourceKey := WatchSourceKey(signal.Source, signal.ConnectionID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE standing_inbox SET signal=? WHERE signal IN
		(SELECT body FROM standing_signals WHERE source=? AND scope=? AND event_key=?)`,
		string(body), sourceKey, signal.Scope, signal.Key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE standing_signals SET body=? WHERE source=? AND scope=? AND event_key=?",
		string(body), sourceKey, signal.Scope, signal.Key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) enqueueStandingWatch(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id, chat, bud, raw, assignment string
	var budget int
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.conversation,i.bud,i.signal,i.assignment,i.budget
		FROM standing_inbox i JOIN standing_watches w ON w.id=i.watch_id
		WHERE i.state='pending' AND w.enabled=1 ORDER BY i.created,i.id LIMIT 1`).
		Scan(&id, &chat, &bud, &raw, &assignment, &budget)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := checkQueue(ctx, tx, true); err != nil {
		return false, err
	}
	var automatic int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM standing_inbox i JOIN tasks t ON t.id=i.task_id
		WHERE t.status='queued'`).Scan(&automatic); err != nil {
		return false, err
	}
	if automatic >= 10 {
		return false, ErrQueueFull
	}
	agent, err := conversationParticipant(ctx, tx, chat, bud)
	if err != nil {
		return false, err
	}
	var signal StandingSignal
	if err := json.Unmarshal([]byte(raw), &signal); err != nil {
		return false, err
	}
	agent.AllowEdits, agent.WorkspaceNeedsReview, agent.Workspace = false, false, ""
	title := "Watch · " + signal.Title
	if len(title) > 120 {
		title = strings.ToValidUTF8(title[:120], "")
	}
	job, err := enqueueTurn(ctx, tx, BackgroundWork, id, chat, agent, workRequest{
		origin: WatchOrigin, title: title, brief: assignment, timeout: budget * 60,
	})
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE standing_inbox SET state='queued',task_id=? WHERE id=?", job.ID, id); err != nil {
		return false, err
	}
	if err := addEvent(ctx, tx, job.ID, "system", "Watch triggered by "+signal.Title+"\n"+signal.URL); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) StandingInbox(ctx context.Context) ([]InboxItem, error) {
	return s.standingInbox(ctx, "")
}

func (s *Store) StandingInboxItem(ctx context.Context, id string) (InboxItem, error) {
	items, err := s.standingInbox(ctx, id)
	if err != nil {
		return InboxItem{}, err
	}
	if len(items) != 1 {
		return InboxItem{}, ErrNotFound
	}
	return items[0], nil
}

func (s *Store) standingInbox(ctx context.Context, id string) ([]InboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,w.name,i.conversation,a.name,i.signal,
		CASE WHEN t.status IS NULL OR i.state IN ('notified','overflow') THEN i.state ELSE t.status END,COALESCE(i.task_id,''),
		COALESCE(q.task_id,''),COALESCE((SELECT m.text FROM messages m WHERE m.task_id=COALESCE(q.task_id,i.task_id)
		AND m.role='assistant' ORDER BY m.id DESC LIMIT 1),''),
		i.attempts,i.seen,i.created
		FROM standing_inbox i JOIN standing_watches w ON w.id=i.watch_id JOIN agents a ON a.id=i.bud
		LEFT JOIN tasks t ON t.id=i.task_id
		LEFT JOIN work_inputs q ON q.job_id=i.task_id AND q.answer_key IS NULL AND t.status='needs_input'
		WHERE i.state<>'dismissed' AND (?='' OR i.id=?)
		ORDER BY (i.state='approval' OR q.task_id IS NOT NULL) DESC,i.seen,i.created DESC LIMIT 120`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxItem
	for rows.Next() {
		var i InboxItem
		var raw string
		var at int64
		if err := rows.Scan(&i.ID, &i.Watch, &i.ConversationID, &i.Bud, &raw, &i.State, &i.TaskID, &i.InputID, &i.Result, &i.Attempts, &i.Seen, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &i.Signal); err != nil {
			return nil, err
		}
		i.At = time.UnixMicro(at).UTC()
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) UpdateInboxItem(ctx context.Context, id, decision string) error {
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result sql.Result
	switch decision {
	case "approve":
		result, err = tx.ExecContext(ctx, "UPDATE standing_inbox SET state='pending',seen=1 WHERE id=? AND state='approval'", id)
	case "dismiss":
		result, err = tx.ExecContext(ctx, "UPDATE standing_inbox SET state='dismissed',seen=1 WHERE id=? AND state IN ('approval','digest','pending')", id)
	case "read":
		result, err = tx.ExecContext(ctx, "UPDATE standing_inbox SET seen=1 WHERE id=?", id)
	default:
		return &InputError{"Choose approve, dismiss, or read."}
	}
	if err := requireStandingChange(result, err); err != nil {
		return err
	}
	if decision != "read" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM standing_delivery WHERE id=? AND sent=0", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func requeueStandingWork(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status='queued',started=NULL,
		summary='Retrying interrupted watch investigation.'
		WHERE status='running' AND (?='' OR id=?) AND id IN
		(SELECT task_id FROM standing_inbox WHERE attempts<3)`, id, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) RetryInterruptedWatch(ctx context.Context, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	requeued, err := requeueStandingWork(ctx, tx, id)
	if err != nil || !requeued {
		return false, err
	}
	return true, tx.Commit()
}

func addWatchReceipt(ctx context.Context, tx *sql.Tx, task string, status Status) error {
	if status == Cancelled {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE standing_inbox SET seen=0 WHERE task_id=?", task); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO standing_delivery(id,generation,due)
		SELECT id,? || ':' || attempts,? FROM standing_inbox WHERE task_id=?
		ON CONFLICT(id) DO UPDATE SET generation=excluded.generation,due=excluded.due,attempts=0,sent=0`,
		status, time.Now().UTC().UnixMicro(), task)
	return err
}

func (s *Store) StandingMemories(ctx context.Context, agent string) ([]MemoryNote, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,bud,note,source,expires FROM standing_memory WHERE bud=?
		AND (expires=0 OR expires>?) ORDER BY created DESC LIMIT 40`, agent, time.Now().UnixMicro())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryNote
	for rows.Next() {
		var m MemoryNote
		var expiry int64
		if err := rows.Scan(&m.ID, &m.AgentID, &m.Note, &m.Source, &expiry); err != nil {
			return nil, err
		}
		if expiry != 0 {
			m.Expires = time.UnixMicro(expiry).UTC()
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SaveStandingMemory(ctx context.Context, m MemoryNote) (MemoryNote, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryNote{}, err
	}
	defer tx.Rollback()
	m, err = saveStandingMemory(ctx, tx, m)
	if err != nil {
		return m, err
	}
	return m, tx.Commit()
}

func saveStandingMemory(ctx context.Context, tx *sql.Tx, m MemoryNote) (MemoryNote, error) {
	m.Note, m.Source = strings.TrimSpace(m.Note), strings.TrimSpace(m.Source)
	if m.Source == "" {
		m.Source = "Saved by you"
	}
	if !validTitle(m.Source, 100) || m.Note == "" || len(m.Note) > 800 {
		return MemoryNote{}, &InputError{"Enter a note under 800 bytes and a short source."}
	}
	var expiry int64
	if !m.Expires.IsZero() {
		expiry = m.Expires.UnixMicro()
		if expiry <= time.Now().UnixMicro() {
			return MemoryNote{}, &InputError{"Choose an expiry in the future."}
		}
	}
	removed, err := agentRemoved(ctx, tx, m.AgentID)
	if err != nil {
		return MemoryNote{}, err
	}
	if removed {
		return MemoryNote{}, &InputError{"This Bud was removed."}
	}
	if m.ID == "" {
		m.ID = "memo_" + rand.Text()
		now := time.Now().UnixMicro()
		result, err := tx.ExecContext(ctx, `INSERT INTO standing_memory SELECT ?,?,?,?,?,? WHERE
			(SELECT count(*) FROM standing_memory WHERE bud=? AND (expires=0 OR expires>?))<40`,
			m.ID, m.AgentID, m.Note, m.Source, now, expiry, m.AgentID, now)
		err = requireStandingChange(result, err)
		if errors.Is(err, ErrInactive) {
			return MemoryNote{}, &InputError{"This Bud can have up to 40 active memories."}
		}
		if err != nil {
			return m, err
		}
		return m, nil
	}
	result, err := tx.ExecContext(ctx, "UPDATE standing_memory SET note=?,source=?,expires=? WHERE id=? AND bud=?",
		m.Note, m.Source, expiry, m.ID, m.AgentID)
	if err := requireStandingChange(result, err); err != nil {
		return m, err
	}
	return m, nil
}

func (s *Store) DeleteStandingMemory(ctx context.Context, id string) error {
	return deleteStandingMemory(ctx, s.db, id)
}

func deleteStandingMemory(ctx context.Context, db writeQueryer, id string) error {
	result, err := db.ExecContext(ctx, "DELETE FROM standing_memory WHERE id=?", id)
	return requireStandingChange(result, err)
}

func (s *Store) StandingPoll(ctx context.Context, source, scope string) (PollState, error) {
	p := PollState{Source: source, Scope: scope}
	var checked, success int64
	err := s.db.QueryRowContext(ctx, `SELECT cursor,checked,success,problem FROM standing_poll WHERE source=? AND scope=?`,
		source, scope).Scan(&p.Cursor, &checked, &success, &p.Problem)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if checked != 0 {
		p.Checked = time.UnixMicro(checked).UTC()
	}
	if success != 0 {
		p.Success = time.UnixMicro(success).UTC()
	}
	return p, err
}

func (s *Store) RecordStandingPoll(ctx context.Context, p PollState, cursor, problem string) error {
	now := time.Now().UTC().UnixMicro()
	var success int64
	if problem == "" {
		success = now
	} else {
		cursor = p.Cursor
		if !p.Success.IsZero() {
			success = p.Success.UnixMicro()
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO standing_poll VALUES (?,?,?,?,?,?)
		ON CONFLICT(source,scope) DO UPDATE SET cursor=CASE WHEN excluded.problem='' THEN excluded.cursor ELSE standing_poll.cursor END,
		checked=excluded.checked,success=excluded.success,problem=excluded.problem`,
		p.Source, p.Scope, cursor, now, success, problem)
	return err
}

func (s *Store) CheckpointStandingPoll(ctx context.Context, p PollState, cursor string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO standing_poll VALUES (?,?,?,0,0,'')
		ON CONFLICT(source,scope) DO UPDATE SET cursor=excluded.cursor`, p.Source, p.Scope, cursor)
	return err
}

func (s *Store) NoteStandingWebhook(ctx context.Context, scope string) error {
	now := time.Now().UnixMicro()
	_, err := s.db.ExecContext(ctx, `INSERT INTO standing_poll VALUES (?,?, '',?,?,'')
		ON CONFLICT(source,scope) DO UPDATE SET checked=excluded.checked,success=excluded.success,problem=''`,
		GitHubCI, scope, now, now)
	return err
}

func (s *Store) StandingHealth(ctx context.Context) (StandingHealth, error) {
	h := StandingHealth{StartedAt: s.startedAt}
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM tasks WHERE status='queued'),
		(SELECT count(*) FROM tasks WHERE status='running'),
		(SELECT count(*) FROM tasks WHERE status='running' AND started+timeout*1000000<?),
		(SELECT count(*) FROM tasks WHERE status='needs_input' AND kind<>'work_input')+(SELECT count(*) FROM standing_inbox WHERE state='approval'),
		(SELECT count(*) FROM routines WHERE enabled=1 AND next_at<?)`, time.Now().UnixMicro(), time.Now().Add(-time.Minute).UnixMicro()).
		Scan(&h.Queued, &h.Running, &h.Stuck, &h.NeedsInput, &h.Overdue); err != nil {
		return h, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+standingNotifications+` AND d.attempts>=8`).Scan(&h.Exhausted); err != nil {
		return h, err
	}
	var err error
	if h.Workers, err = s.Devices(ctx); err != nil {
		return h, err
	}
	watches, err := s.Watches(ctx)
	if err != nil {
		return h, err
	}
	seen := make(map[[2]string]bool)
	for _, watch := range watches {
		if !watch.Enabled {
			continue
		}
		source := WatchSourceKey(watch.Source, watch.ConnectionID)
		key := [2]string{source, watch.Scope}
		if seen[key] {
			continue
		}
		seen[key] = true
		p, err := s.StandingPoll(ctx, source, watch.Scope)
		if err != nil {
			return h, err
		}
		p.Source, p.ConnectionID = watch.Source, watch.ConnectionID
		h.Feeds = append(h.Feeds, p)
	}
	if p, err := s.StandingPoll(ctx, "snapshot", "database"); err != nil {
		return h, err
	} else if !p.Checked.IsZero() {
		h.Feeds = append(h.Feeds, p)
	}
	return h, nil
}

func (s *Store) Ready(ctx context.Context) error {
	var exists int
	return s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM tasks LIMIT 1)").Scan(&exists)
}

type StandingNotification struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Chat       string `json:"conversation_id"`
	Attempt    int    `json:"-"`
	Generation string `json:"-"`
}

const standingNotifications = ` FROM standing_delivery d JOIN standing_inbox i ON i.id=d.id
	JOIN standing_watches w ON w.id=i.watch_id LEFT JOIN tasks t ON t.id=i.task_id WHERE d.sent=0
	AND i.seen=0 AND i.state<>'dismissed' AND COALESCE(t.status,i.state) NOT IN ('queued','running','pending','cancelling')`

func (s *Store) DueStandingNotification(ctx context.Context) (StandingNotification, error) {
	var n StandingNotification
	err := s.db.QueryRowContext(ctx, `SELECT i.id,w.name,COALESCE(t.status,i.state),i.conversation,d.attempts,d.generation`+
		standingNotifications+` AND d.attempts<8 AND d.due<=? ORDER BY d.due LIMIT 1`,
		time.Now().UnixMicro()).Scan(&n.ID, &n.Title, &n.Kind, &n.Chat, &n.Attempt, &n.Generation)
	return n, err
}

func (s *Store) RetryStandingNotifications(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE standing_delivery SET attempts=0,due=? WHERE id IN
		(SELECT d.id`+standingNotifications+` AND d.attempts>=8)`, time.Now().UnixMicro())
	return err
}

func (s *Store) CompleteStandingNotification(ctx context.Context, n StandingNotification, sent bool) error {
	delay := time.Minute * time.Duration(1<<min(n.Attempt, 6))
	_, err := s.db.ExecContext(ctx, "UPDATE standing_delivery SET attempts=attempts+1,sent=?,due=? WHERE id=? AND generation=?",
		sent, time.Now().Add(delay).UnixMicro(), n.ID, n.Generation)
	return err
}

// CI signals use the same identity for a webhook, a catch-up poll, or a redelivery.
func CIWatchKey(run, attempt int64) string {
	return strconv.FormatInt(run, 10) + ":" + strconv.FormatInt(max(attempt, 1), 10)
}
