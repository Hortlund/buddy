package tasks

import (
	"errors"
	"os"
	"syscall"

	"github.com/ebitengine/purego"
)

func workspaceChangeTime(info os.FileInfo) syscall.Timespec {
	return info.Sys().(*syscall.Stat_t).Ctimespec
}

func copyWorkspaceAttributes(source, destination *os.File) error {
	symbol, err := purego.Dlsym(purego.RTLD_DEFAULT, "fcopyfile")
	if err != nil {
		return err
	}
	var copyFile func(int32, int32, uintptr, uint32) int32
	purego.RegisterFunc(&copyFile, symbol)
	// copyfile.h's COPYFILE_ACL and COPYFILE_XATTR.
	const acl, attributes = 1 << 0, 1 << 2
	flags := uint32(acl | attributes)
	from, to := int32(source.Fd()), int32(destination.Fd())
	// A source without an ACL leaves the destination's inherited entries
	// in place, so clear them before applying the source metadata.
	if err := clearWorkspaceACL(to); err != nil {
		if !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EOPNOTSUPP) {
			return err
		}
		flags = attributes
	}
	if copyFile(from, to, 0, flags) != 0 {
		return errors.New("fcopyfile could not preserve workspace file metadata")
	}
	return nil
}

func clearWorkspaceACL(fd int32) error {
	var initACL func(int32) uintptr
	var freeACL func(uintptr) int32
	setACL, err := purego.Dlsym(purego.RTLD_DEFAULT, "acl_set_fd")
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&initACL, purego.RTLD_DEFAULT, "acl_init")
	purego.RegisterLibFunc(&freeACL, purego.RTLD_DEFAULT, "acl_free")
	empty := initACL(0)
	if empty == 0 {
		return errors.New("could not allocate an empty ACL")
	}
	defer freeACL(empty)
	if result, _, errno := purego.SyscallN(setACL, uintptr(fd), empty); int32(result) != 0 {
		if errno != 0 {
			return syscall.Errno(errno)
		}
		return errors.New("could not clear the staged file’s inherited ACL")
	}
	return nil
}
