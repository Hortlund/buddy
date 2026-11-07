package tasks

import (
	"errors"
	"os"
	"syscall"
)

func openWorkspaceEditSource(root *os.Root, name string, expected os.FileInfo) (*os.File, error) {
	readOnly := &InputError{"This file is read-only or has restricted permissions."}
	if expected.Mode().Perm()&0222 == 0 || expected.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return nil, readOnly
	}
	file, err := root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS) {
			return nil, readOnly
		}
		return nil, ErrWorkspaceFileChanged
	}
	opened, err := file.Stat()
	if err != nil || !workspaceRegularFile(opened) || !sameWorkspaceEditSource(expected, opened) {
		file.Close()
		return nil, ErrWorkspaceFileChanged
	}
	return file, nil
}

func sameWorkspaceEditSource(before, after os.FileInfo) bool {
	return sameQuickFile(before, after) && workspaceChangeTime(before) == workspaceChangeTime(after)
}

func stageWorkspaceEdit(root *os.Root, name, text string, expected os.FileInfo) (string, error) {
	source, err := openWorkspaceEditSource(root, name, expected)
	if err != nil {
		return "", err
	}
	defer source.Close()
	return stageWorkspaceFile(root, text, source)
}

func preserveWorkspaceMetadata(source, destination *os.File) error {
	original, err := source.Stat()
	if err != nil {
		return err
	}
	staged, err := destination.Stat()
	if err != nil {
		return err
	}
	from, to := original.Sys().(*syscall.Stat_t), staged.Sys().(*syscall.Stat_t)
	if from.Uid != to.Uid || from.Gid != to.Gid {
		if err := destination.Chown(int(from.Uid), int(from.Gid)); err != nil {
			return err
		}
	}
	if err := destination.Chmod(original.Mode().Perm()); err != nil {
		return err
	}
	return copyWorkspaceAttributes(source, destination)
}
