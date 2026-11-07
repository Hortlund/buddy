package localconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type CodexKey struct{ Dir string }

func (k CodexKey) Read() (key, source string, err error) {
	b, err := readPrivateFile(filepath.Join(k.Dir, "codex.key"))
	if err == nil {
		return strings.TrimSpace(string(b)), "saved_key", nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	for _, name := range []string{"CODEX_API_KEY", "OPENAI_API_KEY"} {
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return key, "environment_key", nil
		}
	}
	return "", "cli", nil
}

func ValidCodexKey(key string) bool {
	return len(key) >= 8 && len(key) <= 512 && strings.IndexFunc(key, func(r rune) bool {
		return r < '!' || r > '~'
	}) < 0
}

func (k CodexKey) Save(key string) error {
	return writePrivateFile(filepath.Join(k.Dir, "codex.key"), []byte(key+"\n"), true)
}

func (k CodexKey) Remove() error {
	err := os.Remove(filepath.Join(k.Dir, "codex.key"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
