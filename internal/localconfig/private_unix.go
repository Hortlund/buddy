//go:build darwin || linux

package localconfig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// PrivateDir requires the state to be owned by this account and unreachable
// for replacement by other accounts. A trusted sticky directory can contain it.
func PrivateDir(path string) (string, error) {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() {
			return "", fmt.Errorf("invalid Buddy state directory %q", dir)
		}
		uid := uint32(os.Geteuid())
		mode := info.Mode()
		if dir == path {
			if stat.Uid != uid || mode.Perm()&0077 != 0 {
				return "", fmt.Errorf("Buddy state directory %q must be owned by this account and private (0700)", dir)
			}
		} else if (stat.Uid != uid && stat.Uid != 0) || (mode.Perm()&0022 != 0 && mode&os.ModeSticky == 0) {
			return "", fmt.Errorf("unsafe parent directory for Buddy state: %q", dir)
		}
		if filepath.Dir(dir) == dir {
			return path, nil
		}
	}
}

func readPrivateFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Buddy credential file %q must be owned by this account and private", path)
	}
	const limit = 32 << 10
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(data) > limit {
		return nil, errors.New("Buddy credential file is too large")
	}
	return data, err
}
