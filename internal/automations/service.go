package automations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

type Service struct {
	mobile                 *applePush
	store                  *tasks.Store
	config                 localconfig.StandingConfig
	client                 *http.Client
	zone                   *time.Location
	stateDir, workspaceDir string
	snapshotSetupErr       error
}

func New(store *tasks.Store, config localconfig.StandingConfig) *Service {
	zone, _ := time.LoadLocation(config.QuietZone)
	return &Service{store: store, config: config, zone: zone, client: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Service) GitHubDelivery() string {
	if s.config.GitHubToken != "" {
		return "catchup"
	}
	if s.config.GitHubWebhookSecret != "" {
		return "webhook"
	}
	return "unconfigured"
}

func (s *Service) SetSnapshotLocation(state, workspace string) bool {
	s.stateDir, s.workspaceDir = state, workspace
	if s.config.SnapshotDir == "" {
		return false
	}
	s.snapshotSetupErr = s.checkSnapshotOverlap()
	return s.snapshotSetupErr == nil
}

func (s *Service) checkSnapshotOverlap() error {
	for _, path := range []string{s.stateDir, s.workspaceDir} {
		if path == "" {
			continue
		}
		into, _ := filepath.Rel(path, s.config.SnapshotDir)
		out, _ := filepath.Rel(s.config.SnapshotDir, path)
		if filepath.IsLocal(into) || filepath.IsLocal(out) {
			return errors.New("keep automatic snapshots in a private directory separate from state and workspaces")
		}
		overlap, err := tasks.WorkspacePathsOverlap(s.config.SnapshotDir, path)
		if overlap || err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
			return errors.New("keep automatic snapshots in a private directory separate from state and workspaces")
		}
	}
	return nil
}

func (s *Service) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for ctx.Err() == nil {
			s.notify(ctx)
			s.notifyMobile(ctx)
			select {
			case <-ctx.Done():
			case <-ticker.C:
			}
		}
	}()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := s.poll(ctx); err != nil && ctx.Err() == nil {
			log.Printf("Buddy watches: %v", err)
		}
		s.snapshot(ctx)
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	<-done
}

func (s *Service) poll(ctx context.Context) error {
	watches, err := s.store.Watches(ctx)
	if err != nil {
		return err
	}
	type gmailSession struct{ access, problem string }
	gmailSessions := make(map[string]gmailSession)
	polled := make(map[[2]string]bool)
	// Poll each feed from the earliest active watch, including after resume.
	slices.SortStableFunc(watches, func(a, b tasks.Watch) int { return a.ActiveSince.Compare(b.ActiveSince) })
	for _, w := range watches {
		if !w.Enabled || ctx.Err() != nil {
			continue
		}
		source := tasks.WatchSourceKey(w.Source, w.ConnectionID)
		key := [2]string{source, w.Scope}
		if polled[key] {
			continue
		}
		polled[key] = true
		p, err := s.store.StandingPoll(ctx, source, w.Scope)
		if err != nil {
			return err
		}
		cursor, problem := p.Cursor, ""
		pollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		var credential tasks.ProviderCredentials
		if w.ConnectionID != "" {
			credential, err = s.activeConnection(pollCtx, w.ConnectionID)
			if err != nil {
				cancel()
				if errors.Is(err, tasks.ErrNotFound) {
					continue
				}
				var disconnected *tasks.InputError
				if errors.As(err, &disconnected) {
					continue
				}
				return err
			}
		}
		switch w.Source {
		case tasks.GitHubCI:
			if w.ConnectionID == "" {
				credential.Secret = s.config.GitHubToken
			}
			if credential.Secret == "" {
				if s.config.GitHubWebhookSecret != "" {
					cancel()
					continue
				}
				problem = "Configure a GitHub token or signed webhook on this server."
			} else {
				cursor, problem = s.pollGitHub(pollCtx, w, p, credential.Secret)
			}
		case tasks.GmailLabel:
			if w.ConnectionID == "" {
				credential.Secret = s.config.GmailRefreshToken
				credential.ClientID = s.config.GmailClientID
				credential.ClientSecret = s.config.GmailClientSecret
			}
			if credential.Secret == "" {
				problem = "Configure Gmail OAuth credentials on this server."
				break
			}
			session, ok := gmailSessions[w.ConnectionID]
			if !ok {
				session.access, session.problem = s.gmailToken(pollCtx, credential)
				gmailSessions[w.ConnectionID] = session
			}
			problem = session.problem
			if problem == "" {
				cursor, problem = s.pollGmail(pollCtx, session.access, w, p)
			}
		case tasks.GrafanaAlerts, tasks.PrometheusAlerts:
			problem = s.pollMonitoring(pollCtx, credential, w)
		}
		cancel()
		if err := s.store.RecordStandingPoll(ctx, p, cursor, problem); err != nil {
			return err
		}
	}
	return nil
}

type responseError int

func (e responseError) Error() string { return fmt.Sprintf("provider returned HTTP %d", e) }

func (s *Service) getJSON(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return errors.New("provider connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return responseError(res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out); err != nil {
		return errors.New("invalid provider response")
	}
	return nil
}

func (s *Service) quiet(now time.Time) bool {
	start, end := s.config.QuietStart, s.config.QuietEnd
	if start == end || s.zone == nil {
		return false
	}
	hour := now.In(s.zone).Hour()
	if start < end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
}

func (s *Service) notify(ctx context.Context) {
	if s.config.NotifyURL == "" || s.quiet(time.Now()) {
		return
	}
	for range 10 {
		n, err := s.store.DueStandingNotification(ctx)
		if errors.Is(err, sql.ErrNoRows) || ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("Buddy notification queue: %v", err)
			return
		}
		body, _ := json.Marshal(n)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.NotifyURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", n.ID+":"+n.Generation)
		if s.config.NotifyBearer != "" {
			req.Header.Set("Authorization", "Bearer "+s.config.NotifyBearer)
		}
		res, err := s.client.Do(req)
		sent := err == nil && res.StatusCode >= 200 && res.StatusCode < 300
		if res != nil {
			res.Body.Close()
		}
		if err := s.store.CompleteStandingNotification(ctx, n, sent); err != nil {
			log.Printf("record Buddy notification: %v", err)
			return
		}
		if !sent {
			return
		}
	}
}

func (s *Service) snapshot(ctx context.Context) {
	if s.config.SnapshotDir == "" || ctx.Err() != nil {
		return
	}
	p, err := s.store.StandingPoll(ctx, "snapshot", "database")
	if err != nil || time.Since(p.Success) < 24*time.Hour || time.Since(p.Checked) < 15*time.Minute {
		return
	}
	problem := ""
	if err := s.writeSnapshot(ctx); err != nil {
		log.Printf("Buddy database snapshot: %v", err)
		problem = "Database snapshot failed. Check the server log."
	}
	if err := s.store.RecordStandingPoll(ctx, p, "", problem); err != nil && ctx.Err() == nil {
		log.Printf("record Buddy snapshot health: %v", err)
	}
}

func (s *Service) writeSnapshot(ctx context.Context) error {
	if s.snapshotSetupErr != nil {
		return s.snapshotSetupErr
	}
	dir, err := localconfig.PrivateDir(s.config.SnapshotDir)
	if err != nil {
		return err
	}
	if dir != s.config.SnapshotDir {
		return errors.New("snapshot directory resolved to a different location")
	}
	if err := s.checkSnapshotOverlap(); err != nil {
		return err
	}
	keep := s.config.SnapshotKeep
	if keep == 0 {
		keep = 7
	}
	if err := pruneSnapshots(dir, "", keep); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".buddy-snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	file := filepath.Join(stage, "buddy.db")
	if err := s.store.SnapshotOnline(ctx, file); err != nil {
		return err
	}
	if err := os.Chmod(file, 0600); err != nil {
		return err
	}
	suffix := filepath.Base(stage)[len(".buddy-snapshot-"):]
	name := "buddy-" + time.Now().UTC().Format("20060102T150405Z") + "-" + suffix + ".db"
	if err := os.Rename(file, filepath.Join(dir, name)); err != nil {
		return err
	}
	if err := pruneSnapshots(dir, name, keep); err != nil {
		return errors.Join(err, os.Remove(filepath.Join(dir, name)))
	}
	return nil
}

var snapshotName = regexp.MustCompile(`^buddy-[0-9]{8}T[0-9]{6}Z-[0-9]+\.db$`)

func pruneSnapshots(dir, newest string, keep int) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	var names []string
	if newest != "" {
		keep--
	}
	for _, entry := range entries {
		if entry.Name() != newest && entry.Type().IsRegular() && snapshotName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	for _, name := range names[:max(0, len(names)-keep)] {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
