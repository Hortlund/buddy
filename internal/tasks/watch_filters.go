package tasks

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/mail"
	"slices"
	"strconv"
	"strings"
)

type WatchFilter struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

func encodeWatchFilters(source string, filters []WatchFilter) (string, error) {
	if len(filters) > 4 {
		return "", &InputError{"Use up to four filters per watch."}
	}
	normalized := make([]WatchFilter, 0, len(filters))
	for _, f := range filters {
		f.Value = strings.TrimSpace(f.Value)
		valid := f.Field == "text"
		switch source {
		case GitHubCI:
			valid = valid || f.Field == "branch" || f.Field == "workflow"
		case GmailLabel:
			valid = valid || f.Field == "sender" || f.Field == "subject"
		case GrafanaAlerts, PrometheusAlerts:
			valid = valid || f.Field == "alert" || f.Field == "severity"
		}
		if !valid || !validTitle(f.Value, 200) {
			return "", &InputError{"Choose a supported watch filter with a value under 200 bytes."}
		}
		if f.Field == "sender" {
			addr, err := mail.ParseAddress(f.Value)
			if err != nil {
				return "", &InputError{"Use an email address for the sender filter."}
			}
			f.Value = addr.Address
		}
		if f.Field != "branch" {
			f.Value = strings.ToLower(f.Value)
		}
		if !slices.Contains(normalized, f) {
			normalized = append(normalized, f)
		}
	}
	slices.SortFunc(normalized, func(a, b WatchFilter) int {
		return cmp.Or(cmp.Compare(a.Field, b.Field), cmp.Compare(a.Value, b.Value))
	})
	data, err := json.Marshal(normalized)
	return string(data), err
}

func (watch Watch) matches(signal StandingSignal) bool {
	var hasHistoryBoundary bool
	if watch.GmailHistory != "" && signal.GmailHistory != "" {
		event, eventErr := strconv.ParseUint(signal.GmailHistory, 10, 64)
		since, sinceErr := strconv.ParseUint(watch.GmailHistory, 10, 64)
		hasHistoryBoundary = eventErr == nil && sinceErr == nil
		if hasHistoryBoundary && event <= since {
			return false
		}
	}
	if !hasHistoryBoundary && !signal.OccurredAt.IsZero() && signal.OccurredAt.Before(watch.ActiveSince) {
		return false
	}
	text := strings.ToLower(signal.Title + "\n" + signal.Detail)
	if watch.Ignore != "" && strings.Contains(text, strings.ToLower(watch.Ignore)) {
		return false
	}
	for _, filter := range watch.Filters {
		matched := false
		switch filter.Field {
		case "text":
			matched = strings.Contains(text, filter.Value)
		case "branch":
			matched = signal.Branch == filter.Value
		case "workflow":
			matched = strings.EqualFold(signal.Workflow, filter.Value)
		case "sender":
			address, err := mail.ParseAddress(signal.Sender)
			matched = err == nil && strings.EqualFold(address.Address, filter.Value)
		case "subject":
			matched = strings.Contains(strings.ToLower(signal.Subject), filter.Value)
		case "alert":
			matched = strings.EqualFold(signal.Alert, filter.Value)
		case "severity":
			matched = strings.EqualFold(signal.Severity, filter.Value)
		}
		if !matched {
			return false
		}
	}
	return true
}

func migrateWatchFilters(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	const unique = `CREATE UNIQUE INDEX IF NOT EXISTS standing_watches_active_filter
		ON standing_watches(source,scope,filters) WHERE enabled>=0`
	var upgraded, hasFilters bool
	err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('standing_watches') WHERE name='activated_at'),
		EXISTS(SELECT 1 FROM pragma_table_info('standing_watches') WHERE name='filters')`).Scan(&upgraded, &hasFilters)
	if err != nil {
		return err
	}
	if upgraded {
		_, err := conn.ExecContext(ctx, unique)
		return err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	filters := "'[]'"
	if hasFilters {
		filters = "filters"
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE standing_watches_upgrade"+watchTable+`
		INSERT INTO standing_watches_upgrade(`+watchColumns+`)
			SELECT id,conversation,name,source,scope,mode,instructions,ignore,budget,enabled,created,`+filters+`,created,'' FROM standing_watches;
		DROP TABLE standing_watches;
		ALTER TABLE standing_watches_upgrade RENAME TO standing_watches;
	`+unique); err != nil {
		return err
	}
	var broken string
	if err := tx.QueryRowContext(ctx, `SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&broken); err == nil {
		return errors.New("watch upgrade found a broken reference in " + broken)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	return err
}
