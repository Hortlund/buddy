package localconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// VoiceKey is independent of the Codex CLI's credentials.
type VoiceKey struct{ Dir string }

func (k VoiceKey) Read() (key, source string, err error) {
	b, err := readPrivateFile(filepath.Join(k.Dir, "voice.key"))
	if err == nil {
		return strings.TrimSpace(string(b)), "saved_key", nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if key := strings.TrimSpace(os.Getenv("BUDDY_VOICE_API_KEY")); key != "" {
		return key, "environment_key", nil
	}
	return "", "", nil
}

func (k VoiceKey) Save(key string) error {
	return writePrivateFile(filepath.Join(k.Dir, "voice.key"), []byte(key+"\n"), true)
}

func (k VoiceKey) Remove() error {
	err := os.Remove(filepath.Join(k.Dir, "voice.key"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
