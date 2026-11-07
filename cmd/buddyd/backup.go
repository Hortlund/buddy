package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

var backupStateFiles = [...]string{"buddy.db", "server.token", "server.crt", "server.identity"}

const backupBytesLimit = 16 << 30
const backupEntriesLimit = 250_000

func exportBackup(ctx context.Context, dir, output string, retire bool) error {
	if _, err := os.Lstat(filepath.Join(dir, "buddy.db")); err != nil {
		return fmt.Errorf("open source Buddy database: %w", err)
	}
	lock, err := localconfig.LockDir(dir)
	if err != nil {
		return fmt.Errorf("stop the source Buddy server before making a move backup: %w", err)
	}
	defer lock.Close()
	dir = filepath.Dir(lock.Name())
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	workspace, err := store.ManagedWorkspaceRoot(ctx)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return err
	}
	if filepath.Base(filepath.Dir(output)) == localconfig.MigrationDirName {
		expected, err := localconfig.MigrationDir()
		if err != nil {
			return err
		}
		if private, err := localconfig.PrivateDir(parent); err != nil || private != expected {
			return errors.New("use a real, private .buddy-migration folder directly under your home directory")
		}
	}
	if err := outsideCheckout(parent); err != nil {
		return err
	}
	for _, source := range []string{dir, workspace} {
		if source != "" && withinBackupRoot(source, parent) {
			return errors.New("choose a backup destination outside the source state and managed workspaces")
		}
	}
	output = filepath.Join(parent, filepath.Base(output))
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("choose a new backup file")
	}
	tmp, err := os.MkdirTemp(parent, ".buddy-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, "buddy.db")
	if err := store.Snapshot(ctx, snapshot); err != nil {
		return err
	}
	path := filepath.Join(tmp, "bundle")
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	archive := &backupWriter{tar: tar.NewWriter(gz)}
	err = archive.add(ctx, snapshot, "state/buddy.db")
	for _, file := range backupStateFiles[1:] {
		if err == nil {
			err = archive.add(ctx, filepath.Join(dir, file), "state/"+file)
		}
	}
	profiles := filepath.Join(dir, "profiles")
	if _, statErr := os.Lstat(profiles); err == nil && !errors.Is(statErr, os.ErrNotExist) {
		err = archive.add(ctx, profiles, "state/profiles")
	}
	if err == nil && workspace != "" {
		err = archive.add(ctx, workspace, "workspaces")
	}
	if err := errors.Join(err, archive.tar.Close(), gz.Close(), out.Sync(), out.Close()); err != nil {
		return err
	}
	marked := false
	if retire {
		if err := localconfig.RetireHome(dir, output); err == nil {
			marked = true
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if err := os.Link(path, output); err != nil {
		if marked {
			err = errors.Join(err, os.Remove(filepath.Join(dir, localconfig.RetiredHomeMarker)))
		}
		return err
	}
	return nil
}

func outsideCheckout(dir string) error {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return errors.New("choose a location outside the Git checkout for Buddy migration data")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func withinBackupRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

type backupWriter struct {
	tar     *tar.Writer
	size    int64
	entries int
}

func (b *backupWriter) add(ctx context.Context, path, name string) error {
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		target := name
		if relative != "." {
			target += "/" + filepath.ToSlash(relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !validBackupPath(target) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("backup requires regular files and folders: %s", current)
		}
		if err := b.count(info.Size()); err != nil {
			return err
		}
		header := &tar.Header{Name: target, Mode: 0600 | int64(info.Mode().Perm()&0100), Size: info.Size(), Typeflag: tar.TypeReg, ModTime: info.ModTime()}
		if info.IsDir() {
			header.Mode, header.Size, header.Typeflag = 0700, 0, tar.TypeDir
		}
		if err := b.tar.WriteHeader(header); err != nil || info.IsDir() {
			return err
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		defer file.Close()
		actual, err := file.Stat()
		if err != nil || !os.SameFile(info, actual) {
			return fmt.Errorf("file changed while making backup: %s", current)
		}
		_, err = io.CopyN(b.tar, file, info.Size())
		return err
	})
}

func (b *backupWriter) count(size int64) error {
	b.entries++
	if size < 0 || size > backupBytesLimit-b.size || b.entries > backupEntriesLimit {
		return errors.New("Buddy backup exceeds 16 GB or 250,000 entries")
	}
	b.size += size
	return nil
}

func validBackupPath(path string) bool {
	if !fs.ValidPath(path) || strings.ContainsAny(path, "\\:") {
		return false
	}
	if path == "workspaces" || strings.HasPrefix(path, "workspaces/") ||
		path == "state/profiles" || strings.HasPrefix(path, "state/profiles/") {
		return true
	}
	for _, file := range backupStateFiles {
		if path == "state/"+file {
			return true
		}
	}
	return false
}

func restoreBackup(ctx context.Context, archivePath, destination string) error {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	if err := outsideCheckout(parent); err != nil {
		return err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("restore into a new folder")
	}
	in, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer gz.Close()
	gz.Multistream(false)
	temp, err := os.MkdirTemp(parent, ".buddy-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	root, err := os.OpenRoot(temp)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, dir := range []string{"state", "workspaces"} {
		if err := root.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	reader := tar.NewReader(gz)
	var budget backupWriter
	seen := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !validBackupPath(h.Name) || seen[h.Name] || (h.Name == "workspaces" || h.Name == "state/profiles") && h.Typeflag != tar.TypeDir {
			return errors.New("invalid path or duplicate entry in Buddy backup")
		}
		seen[h.Name] = true
		if err := budget.count(h.Size); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if h.Name != "workspaces" {
				if err := root.Mkdir(h.Name, 0700); err != nil {
					return err
				}
			}
		case tar.TypeReg:
			file, err := root.OpenFile(h.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|fs.FileMode(h.Mode)&0100)
			if err != nil {
				return err
			}
			_, err = io.CopyN(file, reader, h.Size)
			if err := errors.Join(err, file.Close()); err != nil {
				return err
			}
		default:
			return errors.New("unsupported entry in Buddy backup")
		}
	}
	// Consume the gzip footer to verify the checksum and reject trailing payload.
	if n, err := io.CopyN(io.Discard, gz, 1); n != 0 || !errors.Is(err, io.EOF) {
		return errors.New("corrupt or unexpected data after Buddy backup")
	}
	for _, name := range backupStateFiles {
		info, err := root.Stat("state/" + name)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("Buddy backup is missing required server state")
		}
	}
	state := filepath.Join(temp, "state")
	if _, err := localconfig.EnsureIdentity(state); err != nil {
		return err
	}
	if _, err := localconfig.ReadToken(filepath.Join(state, localconfig.TokenName)); err != nil {
		return err
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(state, "buddy.db"))
	if err != nil {
		return err
	}
	if err := errors.Join(store.PrepareRestoredHome(ctx), store.Close()); err != nil {
		return err
	}
	if err := renameNewDirectory(temp, destination); err != nil {
		return fmt.Errorf("publish Buddy restore into new folder: %w", err)
	}
	return nil
}
