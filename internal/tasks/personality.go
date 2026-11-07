package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const profileTemplate = "# Personality\n\nDescribe how this agent should talk and work.\n"

func (s *Store) EnableProfiles(ctx context.Context, root string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	s.profilesRoot = root
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM agents")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		// Existing profiles are validated at the start of their next turn.
		if _, err := os.Lstat(filepath.Join(root, id, "AGENTS.md")); errors.Is(err, os.ErrNotExist) {
			if _, err := s.personality(id); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func (s *Store) personality(id string) (string, error) {
	if s.profilesRoot == "" || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return "", nil
	}
	dir := filepath.Join(s.profilesRoot, id)
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("Couldn't read the agent's personality directory.")
	}
	path := filepath.Join(dir, "AGENTS.md")
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			return s.personality(id)
		}
		if err != nil {
			return "", err
		}
		_, err = file.WriteString(profileTemplate)
		return "", errors.Join(err, file.Close())
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16000 {
		return "", errors.New("The agent's AGENTS.md must be a regular Markdown file under 16 KB.")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errors.New("The agent's AGENTS.md must use UTF-8.")
	}
	if string(data) == profileTemplate {
		return "", nil
	}
	return string(data), nil
}
