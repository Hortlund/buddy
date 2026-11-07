//go:build darwin || linux

package localconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockDir prevents two daemons from processing the same queue or recovering
// one another's in-flight tasks. The kernel releases it on process exit.
func LockDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	dir, err := PrivateDir(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "server.lock")
	fd, err := unix.Open(path, unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_CREAT|unix.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		f.Close()
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(os.Geteuid()) ||
		info.Mode&0077 != 0 || info.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("unsafe Buddy server lock %q", path)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock server data directory: %w (is another Buddy server running?)", err)
	}
	return f, nil
}
