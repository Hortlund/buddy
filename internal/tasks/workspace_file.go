package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const MaxWorkspaceTextBytes = 512 << 10

var ErrWorkspaceFileChanged = errors.New("This file changed on disk. Reload it before saving.")
var ErrWorkspaceFolderChanged = errors.New("This Bud’s working folder changed. Open it again from the Workspace sidebar.")

type WorkspaceItem struct {
	Kind      string           `json:"kind"`
	Entries   []workspaceEntry `json:"entries,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
	Text      string           `json:"text,omitempty"`
	Image     []byte           `json:"image,omitempty"`
	Revision  string           `json:"revision,omitempty"`
	ReadOnly  bool             `json:"read_only,omitempty"`
}

type WorkspaceFileEdit struct {
	Revision string `json:"revision"`
	Text     string `json:"text"`
}

func validWorkspaceItemPath(relative string) bool {
	if len(relative) > 2048 || !fs.ValidPath(relative) || strings.ContainsAny(relative, "\\:") ||
		strings.IndexFunc(relative, unicode.IsControl) >= 0 {
		return false
	}
	for part := range strings.SplitSeq(relative, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func (r *CodexRuntime) openWorkspaceItem(ctx context.Context, s *Store, id, workspace, relative string) (*os.Root, error) {
	if relative != "." && !validWorkspaceItemPath(relative) {
		return nil, &InputError{"Choose a file or folder inside this Bud’s workspace."}
	}
	agent, err := s.Agent(ctx, id)
	if err != nil {
		return nil, err
	}
	if agent.WorkspaceNeedsReview {
		return nil, &InputError{WorkspaceReviewMessage}
	}
	if workspace != "" && workspace != agent.Workspace {
		return nil, ErrWorkspaceFolderChanged
	}
	if agent.Workspace == "" {
		return nil, &InputError{"This Bud doesn’t have a working folder."}
	}
	protected := slices.Concat(r.protected, r.readOnly, []string{r.migrationDir, r.snapshotDir, s.profilesRoot})
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	if resolved, err := filepath.EvalSymlinks(codexHome); codexHome != "" && err == nil {
		protected = append(protected, resolved)
	}
	parent, _, err := openQuickWorkspace(agent.Workspace, protected)
	if err != nil {
		return nil, err
	}
	if dir := path.Dir(relative); dir != "." {
		for name := range strings.SplitSeq(dir, "/") {
			child, err := openWorkspaceFolder(parent, name)
			parent.Close()
			if err != nil {
				return nil, err
			}
			parent = child
		}
	}
	return parent, nil
}

func openWorkspaceFolder(root *os.Root, name string) (*os.Root, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() {
		return nil, &InputError{"This folder is unavailable."}
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return nil, &InputError{"This folder is unavailable."}
	}
	opened, err := dir.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		dir.Close()
		return nil, &InputError{"This folder changed. Try opening it again."}
	}
	return dir, nil
}

func (r *CodexRuntime) WorkspaceItem(ctx context.Context, s *Store, id, workspace, relative string) (WorkspaceItem, error) {
	var item WorkspaceItem
	parent, err := r.openWorkspaceItem(ctx, s, id, workspace, relative)
	if err != nil {
		return item, err
	}
	defer parent.Close()
	name := path.Base(relative)
	info, err := parent.Lstat(name)
	if err != nil {
		return item, &InputError{"This file or folder is unavailable."}
	}
	if info.IsDir() {
		folder, err := openWorkspaceFolder(parent, name)
		if err != nil {
			return item, err
		}
		defer folder.Close()
		item.Kind = "folder"
		item.Entries, item.Truncated, err = workspaceEntries(folder)
		slices.SortFunc(item.Entries, func(a, b workspaceEntry) int {
			if a.Folder != b.Folder {
				if a.Folder {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		return item, err
	}
	item.Kind = "unsupported"
	if !workspaceRegularFile(info) || info.Size() > MaxAttachmentBytes {
		return item, nil
	}
	data, info, err := readWorkspaceFile(parent, name, MaxAttachmentBytes)
	if err != nil {
		return WorkspaceItem{}, err
	}
	switch media := http.DetectContentType(data); {
	case visualAttachment(media):
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err == nil && cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= 10000 && cfg.Height <= 10000 &&
			int64(cfg.Width)*int64(cfg.Height) <= 25_000_000 {
			item.Kind, item.Image = "image", data
		}
	case strings.HasPrefix(media, "text/") && len(data) <= MaxWorkspaceTextBytes && validWorkspaceText(string(data)):
		item.Kind, item.Text = "text", string(data)
		item.Revision = workspaceRevision(id, relative, info, data)
		writable, err := openWorkspaceEditSource(parent, name, info)
		item.ReadOnly = err != nil
		if writable != nil {
			writable.Close()
		}
	}
	return item, nil
}

func workspaceRegularFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Nlink == 1
}

func readWorkspaceFile(root *os.Root, name string, limit int) ([]byte, os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil || !workspaceRegularFile(info) || info.Size() > int64(limit) {
		return nil, nil, &InputError{"This file is unavailable for preview or editing."}
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, &InputError{"Couldn’t open this file."}
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !workspaceRegularFile(opened) || !sameQuickFile(info, opened) {
		return nil, nil, ErrWorkspaceFileChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, nil, err
	}
	current, err := root.Lstat(name)
	if err != nil || !workspaceRegularFile(current) || !sameQuickFile(info, current) || len(data) > limit {
		return nil, nil, ErrWorkspaceFileChanged
	}
	return data, current, nil
}

func validWorkspaceText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsRune(text, 0) &&
		strings.HasPrefix(http.DetectContentType([]byte(text[:min(len(text), 512)])), "text/")
}

func workspaceRevision(id, relative string, info os.FileInfo, data []byte) string {
	stat := info.Sys().(*syscall.Stat_t)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d:%d:%d:%d\x00", id, relative, stat.Dev, stat.Ino, info.ModTime().UnixNano(), info.Mode())
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (r *CodexRuntime) SaveWorkspaceFile(ctx context.Context, s *Store, id, workspace, relative string, edit WorkspaceFileEdit) (string, error) {
	if len(edit.Revision) != sha256.Size*2 || len(edit.Text) > MaxWorkspaceTextBytes || !validWorkspaceText(edit.Text) {
		return "", &InputError{"Save a UTF-8 text file up to 512 KB after opening it in Buddy."}
	}
	workspaceFileWrites.Lock()
	defer workspaceFileWrites.Unlock()
	parent, err := r.openWorkspaceItem(ctx, s, id, workspace, relative)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	name := path.Base(relative)
	data, info, err := readWorkspaceFile(parent, name, MaxWorkspaceTextBytes)
	if err != nil || !validWorkspaceText(string(data)) {
		return "", ErrWorkspaceFileChanged
	}
	revision := workspaceRevision(id, relative, info, data)
	if string(data) == edit.Text {
		return revision, nil
	}
	if edit.Revision != revision {
		return "", ErrWorkspaceFileChanged
	}
	staged, err := stageWorkspaceEdit(parent, name, edit.Text, info)
	if err != nil {
		return "", err
	}
	defer parent.Remove(staged)
	saved, err := parent.Lstat(staged)
	if err != nil || !workspaceRegularFile(saved) {
		return "", ErrWorkspaceFileChanged
	}
	// Reopen from the currently configured workspace to detect a move, then
	// verify the contents again as close to the replacement as possible.
	current, err := r.openWorkspaceItem(ctx, s, id, workspace, relative)
	if err != nil {
		return "", err
	}
	defer current.Close()
	beforeDir, beforeErr := parent.Stat(".")
	currentDir, currentErr := current.Stat(".")
	if beforeErr != nil || currentErr != nil || !os.SameFile(beforeDir, currentDir) {
		return "", ErrWorkspaceFileChanged
	}
	latest, latestInfo, err := readWorkspaceFile(current, name, MaxWorkspaceTextBytes)
	if err != nil || !sameWorkspaceEditSource(info, latestInfo) || !bytes.Equal(data, latest) {
		return "", ErrWorkspaceFileChanged
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := parent.Rename(staged, name); err != nil {
		return "", err
	}
	return workspaceRevision(id, relative, saved, []byte(edit.Text)), nil
}
