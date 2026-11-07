package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
)

const (
	MaxAttachments     = 5
	MaxAttachmentBytes = 8 << 20 // total per message
)

type Attachment struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int    `json:"size"`
	Data []byte `json:"data,omitempty"`
}

const attachmentSchema = `
CREATE TABLE IF NOT EXISTS chat_attachments (
	task_id TEXT NOT NULL REFERENCES chat_turns(task_id),
	position INTEGER NOT NULL,
	name TEXT NOT NULL,
	media_type TEXT NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY(task_id,position)
);
CREATE TABLE IF NOT EXISTS handoff_attachments (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	source_task_id TEXT NOT NULL REFERENCES chat_turns(task_id)
);
CREATE TABLE IF NOT EXISTS generated_images (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	data BLOB NOT NULL
);`

func PrepareAttachments(files []Attachment) ([]Attachment, error) {
	if len(files) > MaxAttachments {
		return nil, &InputError{"Attach up to five files per message."}
	}
	out := slices.Clone(files)
	total := 0
	for i := range out {
		f := &out[i]
		f.Name = strings.TrimSpace(f.Name)
		if f.Name == "" || f.Name == "." || f.Name == ".." || len(f.Name) > 200 ||
			strings.ContainsAny(f.Name, "/\\:") || !utf8.ValidString(f.Name) || strings.IndexFunc(f.Name, unicode.IsControl) >= 0 {
			return nil, &InputError{"Use a file name without slashes or special path characters."}
		}
		total += len(f.Data)
		if total > MaxAttachmentBytes {
			return nil, &InputError{"Attachments can total up to 8 MB per message."}
		}
		f.Size, f.Type = len(f.Data), http.DetectContentType(f.Data)
		if visualAttachment(f.Type) {
			cfg, _, err := image.DecodeConfig(bytes.NewReader(f.Data))
			if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 10000 || cfg.Height > 10000 ||
				int64(cfg.Width)*int64(cfg.Height) > 25_000_000 {
				return nil, &InputError{"Choose a valid image under 25 megapixels."}
			}
		}
	}
	return out, nil
}

func visualAttachment(media string) bool {
	switch media {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func readAttachments(ctx context.Context, db queryer, task string) ([]Attachment, error) {
	rows, err := db.QueryContext(ctx, "SELECT name,media_type,data FROM chat_attachments WHERE task_id=? ORDER BY position", task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var f Attachment
		if err := rows.Scan(&f.Name, &f.Type, &f.Data); err != nil {
			return nil, err
		}
		f.Size = len(f.Data)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) Attachment(ctx context.Context, task string, position int) (Attachment, error) {
	var f Attachment
	err := s.db.QueryRowContext(ctx, "SELECT name,media_type,data FROM chat_attachments WHERE task_id=? AND position=?", task, position).
		Scan(&f.Name, &f.Type, &f.Data)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	f.Size = len(f.Data)
	return f, err
}

func (s *Store) GeneratedImage(ctx context.Context, task string) (Attachment, error) {
	image := Attachment{Name: GeneratedImageName, Type: "image/png"}
	err := s.db.QueryRowContext(ctx, "SELECT data FROM generated_images WHERE task_id=?", task).Scan(&image.Data)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	image.Size = len(image.Data)
	return image, err
}

func AttachmentDigest(files []Attachment) [32]byte {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%d:%s%d:", len(f.Name), f.Name, len(f.Data))
		h.Write(f.Data)
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func requestAttachmentSource(ctx context.Context, db queryer, task string) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `
		WITH RECURSIVE requests(id) AS (
			VALUES (?)
			UNION SELECT q.task_id FROM choice_cards q JOIN requests r ON q.answer_task_id=r.id
		)
		SELECT t.id FROM requests r JOIN tasks t ON t.id=r.id
		WHERE t.status NOT IN ('cancelled','cancelling')
		  AND EXISTS(SELECT 1 FROM chat_attachments f WHERE f.task_id=t.id)
		ORDER BY t.rowid DESC LIMIT 1`, task).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func handoffAttachmentSource(ctx context.Context, db queryer, task, parent string) (string, error) {
	var source string
	err := db.QueryRowContext(ctx, "SELECT source_task_id FROM handoff_attachments WHERE task_id=?", task).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) && parent != "" {
		// Handoffs queued by an older server only supported direct uploads
		// and files from the original request behind a clarification card.
		return requestAttachmentSource(ctx, db, parent)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return source, err
}

type stagedAttachment struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Preview string `json:"text_excerpt,omitempty"`
}

func stageAttachments(base string, files []Attachment) (root string, staged []stagedAttachment, images []string, err error) {
	if len(files) == 0 {
		return "", nil, nil, nil
	}
	files, err = PrepareAttachments(files)
	if err != nil {
		return "", nil, nil, err
	}
	temp, err := os.MkdirTemp(base, "run-")
	if err != nil {
		return "", nil, nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(temp)
		}
	}()
	root, err = filepath.EvalSymlinks(temp)
	if err != nil {
		return "", nil, nil, err
	}
	for i, f := range files {
		name := fmt.Sprintf("%d-%s", i+1, f.Name)
		if visualAttachment(f.Type) {
			ext := strings.TrimPrefix(f.Type, "image/")
			name = fmt.Sprintf("image-%d.%s", i+1, ext)
		}
		path := filepath.Join(root, name)
		if err = os.WriteFile(path, f.Data, 0400); err != nil {
			return "", nil, nil, err
		}
		item := stagedAttachment{Name: f.Name, Path: path, Type: f.Type}
		if strings.HasPrefix(f.Type, "text/plain") {
			item.Preview = strings.ToValidUTF8(string(f.Data[:min(3000, len(f.Data))]), "")
		}
		staged = append(staged, item)
		if visualAttachment(f.Type) {
			images = append(images, path)
		}
	}
	return root, staged, images, nil
}
