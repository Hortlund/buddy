package tasks

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const MaxWorkArtifactBytes = 4 << 20

type WorkArtifact struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Bytes     int    `json:"bytes"`
	URL       string `json:"url"`
	Data      []byte `json:"data,omitempty"`
}

func workArtifacts(ctx context.Context, db queryer, job string) ([]WorkArtifact, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,name,media_type,length(data) FROM work_artifacts WHERE task_id=? ORDER BY name", job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WorkArtifact
	for rows.Next() {
		var item WorkArtifact
		if err := rows.Scan(&item.ID, &item.Name, &item.MediaType, &item.Bytes); err != nil {
			return nil, err
		}
		item.URL = "buddy://artifacts/" + job + "?file=" + url.QueryEscape(item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) WorkArtifact(ctx context.Context, job, id string) (WorkArtifact, error) {
	var item WorkArtifact
	err := s.db.QueryRowContext(ctx, "SELECT id,name,media_type,data FROM work_artifacts WHERE task_id=? AND id=?", job, id).Scan(&item.ID, &item.Name, &item.MediaType, &item.Data)
	item.Bytes = len(item.Data)
	return item, err
}

func (s *Store) SaveWorkArtifact(ctx context.Context, job, device, lease string, item WorkArtifact) error {
	if !validTitle(item.Name, 180) || strings.ContainsAny(item.Name, "/\\") || strings.HasPrefix(item.Name, ".") || len(item.Data) > MaxWorkArtifactBytes {
		return &InputError{"Use a named artifact up to 4 MB."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t LEFT JOIN device_runs dr ON dr.task_id=t.id
		WHERE t.id=? AND t.status='running' AND ((?='' AND dr.task_id IS NULL) OR (dr.device_id=? AND dr.lease=? AND dr.expires>?)))`, job, device, device, lease, time.Now().UnixMicro()).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ErrInactive
	}
	var count, total int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(data)),0) FROM work_artifacts WHERE task_id=? AND name<>?", job, item.Name).Scan(&count, &total); err != nil {
		return err
	}
	if count >= 20 || total+len(item.Data) > 16<<20 {
		return &InputError{"This assignment's artifact limit has been reached."}
	}
	item.MediaType = http.DetectContentType(item.Data)
	_, err = tx.ExecContext(ctx, `INSERT INTO work_artifacts VALUES (?,?,?,?,?,?)
		ON CONFLICT(task_id,name) DO UPDATE SET media_type=excluded.media_type,data=excluded.data`, "art_"+rand.Text(), job, item.Name, item.MediaType, item.Data, time.Now().UnixMicro())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Only the dedicated outbox is exported. Never scan a checkout or home directory.
func CollectWorkArtifacts(outbox string) ([]WorkArtifact, error) {
	root, err := os.OpenRoot(outbox)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(21)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 20 {
		return nil, &InputError{"Put at most 20 deliverables in the artifact outbox."}
	}
	var items []WorkArtifact
	total := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		data, _, err := readWorkspaceFile(root, entry.Name(), MaxWorkArtifactBytes)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > 16<<20 {
			return nil, &InputError{"Keep deliverables below 16 MB in total."}
		}
		items = append(items, WorkArtifact{Name: entry.Name(), Data: data})
	}
	return items, nil
}

func ArtifactOutbox(workspace, job string) (string, error) {
	if !mcpName.MatchString(job) {
		return "", &InputError{"Invalid work ID."}
	}
	workspace, err := checkWorkspace(workspace)
	if err != nil {
		return "", err
	}
	outbox := filepath.Join(workspace, ".buddy-work", job, "artifacts")
	if err := os.MkdirAll(outbox, 0700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(outbox)
	if err != nil || resolved != outbox {
		return "", &InputError{"The artifact outbox must stay inside its assignment workspace."}
	}
	return outbox, nil
}
