package localconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// StandingConfig is kept in the private server state directory and is never
// copied into a worker's environment or model context.
type StandingConfig struct {
	MobilePush          *MobilePushConfig `json:"iphone_push"`
	GitHubWebhookSecret string            `json:"github_webhook_secret"`
	GitHubToken         string            `json:"github_token"`
	GitHubLogs          bool              `json:"github_logs"`
	GmailClientID       string            `json:"gmail_client_id"`
	GmailClientSecret   string            `json:"gmail_client_secret"`
	GmailRefreshToken   string            `json:"gmail_refresh_token"`
	NotifyURL           string            `json:"notify_url"`
	NotifyBearer        string            `json:"notify_bearer"`
	QuietZone           string            `json:"quiet_timezone"`
	QuietStart          int               `json:"quiet_start_hour"`
	QuietEnd            int               `json:"quiet_end_hour"`
	SnapshotDir         string            `json:"snapshot_dir"`
	SnapshotKeep        int               `json:"snapshot_keep"`
}

func ReadStandingConfig(dir string) (StandingConfig, error) {
	var c StandingConfig
	data, err := readPrivateFile(filepath.Join(dir, "automations.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return StandingConfig{}, errors.New("automations.json must be a single JSON object with supported settings")
	}
	if c.NotifyURL != "" {
		u, err := url.Parse(c.NotifyURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return StandingConfig{}, errors.New("notify_url must be a valid HTTPS URL")
		}
	}
	if c.QuietStart < 0 || c.QuietStart > 23 || c.QuietEnd < 0 || c.QuietEnd > 23 {
		return StandingConfig{}, errors.New("quiet hours must be from 0 to 23")
	}
	if c.QuietStart != c.QuietEnd {
		if c.QuietZone == "" {
			return StandingConfig{}, errors.New("choose an IANA timezone for quiet hours")
		}
		if _, err := time.LoadLocation(c.QuietZone); err != nil {
			return StandingConfig{}, errors.New("quiet_timezone must be a valid IANA timezone")
		}
	}
	if c.SnapshotDir != "" {
		if !filepath.IsAbs(c.SnapshotDir) || !utf8.ValidString(c.SnapshotDir) || strings.ContainsAny(c.SnapshotDir, "*?[\x00") {
			return StandingConfig{}, errors.New("snapshot_dir must be an absolute, private directory")
		}
		c.SnapshotDir = filepath.Clean(c.SnapshotDir)
		for ancestor := c.SnapshotDir; ; ancestor = filepath.Dir(ancestor) {
			if canonical, err := filepath.EvalSymlinks(ancestor); err == nil {
				relative, err := filepath.Rel(ancestor, c.SnapshotDir)
				if err != nil {
					return StandingConfig{}, err
				}
				c.SnapshotDir = filepath.Join(canonical, relative)
				break
			}
			if ancestor == filepath.Dir(ancestor) {
				break
			}
		}
		if !utf8.ValidString(c.SnapshotDir) || strings.ContainsAny(c.SnapshotDir, "*?[\x00") {
			return StandingConfig{}, errors.New("snapshot_dir resolves to an unsupported path")
		}
	}
	if c.SnapshotKeep < 0 || c.SnapshotKeep > 365 {
		return StandingConfig{}, errors.New("snapshot_keep must be from 1 to 365, or 0 for the default")
	}
	return c, nil
}
