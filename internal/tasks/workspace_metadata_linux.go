package tasks

import (
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func workspaceChangeTime(info os.FileInfo) syscall.Timespec {
	return info.Sys().(*syscall.Stat_t).Ctim
}

func copyWorkspaceAttributes(source, destination *os.File) error {
	from, to := int(source.Fd()), int(destination.Fd())
	names, err := workspaceAttributes(from)
	if err != nil {
		return err
	}
	if slices.Contains(names, "security.capability") {
		return errors.New("workspace file has executable capabilities")
	}
	inherited, err := workspaceAttributes(to)
	if err != nil {
		return err
	}
	for _, name := range inherited {
		if !slices.Contains(names, name) {
			if err := unix.Fremovexattr(to, name); err != nil {
				return err
			}
		}
	}
	for _, name := range names {
		size, err := unix.Fgetxattr(from, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		n, err := unix.Fgetxattr(from, name, value)
		if err != nil {
			return err
		}
		if n > len(value) {
			return unix.ERANGE
		}
		if err := unix.Fsetxattr(to, name, value[:n], 0); err != nil {
			return err
		}
	}
	return nil
}

func workspaceAttributes(fd int) ([]string, error) {
	size, err := unix.Flistxattr(fd, nil)
	if errors.Is(err, unix.ENOTSUP) || err == nil && size == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data := make([]byte, size)
	n, err := unix.Flistxattr(fd, data)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(string(data[:n]), "\x00"), "\x00"), nil
}
