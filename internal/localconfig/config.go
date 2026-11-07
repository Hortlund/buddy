package localconfig

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	Address           = "127.0.0.1:7331"
	TokenName         = "server.token"
	MigrationDirName  = ".buddy-migration"
	RetiredHomeMarker = "move-retired"
)

func DataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Buddy"), nil
}

func MigrationDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) || strings.ContainsAny(home, "*?[") {
		return "", errors.New("Buddy needs an absolute home directory without sandbox glob characters")
	}
	return filepath.Join(home, MigrationDirName), nil
}

func HomeRetired(dir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dir, RetiredHomeMarker))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// EnsureMigrationDir creates the directory that Buddy excludes from agent tools.
func EnsureMigrationDir() (string, error) {
	dir, err := MigrationDir()
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if canonical, err := PrivateDir(dir); err != nil || canonical != dir {
		return "", errors.New("Use a real, private .buddy-migration directory in your home folder.")
	}
	return dir, nil
}

func MoveArchivePath(dir string) string {
	data, err := readPrivateFile(filepath.Join(dir, RetiredHomeMarker))
	var path string
	if err != nil || json.Unmarshal(data, &path) != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == string(filepath.Separator) {
		return ""
	}
	return path
}

// RetireHome is called while holding the daemon lock.
func RetireHome(dir, archive string) (err error) {
	data, err := json.Marshal(archive)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, RetiredHomeMarker)
	if err := writePrivateFile(path, append(data, '\n'), false); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	for _, name := range []string{path, dir} {
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
	}
	return nil
}

func RollbackRetirement(dir string) error {
	lock, err := LockDir(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	return os.Remove(filepath.Join(filepath.Dir(lock.Name()), RetiredHomeMarker))
}

func ReadToken(path string) (string, error) {
	b, err := readPrivateFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if !validToken(token) {
		return "", errors.New("invalid Buddy server token")
	}
	return token, nil
}

func validToken(token string) bool {
	_, err := hex.DecodeString(token)
	return err == nil && len(token) == 64
}

// EnsureToken runs after the server has acquired its data directory lock.
func EnsureToken(dir string) (string, error) {
	path := filepath.Join(dir, TokenName)
	if token, err := ReadToken(path); !errors.Is(err, os.ErrNotExist) {
		return token, err
	}
	return writeToken(path, false)
}

func writeToken(path string, replace bool) (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(secret[:])
	if err := writePrivateFile(path, []byte(token+"\n"), replace); err != nil {
		return "", err
	}
	return token, nil
}
