package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestRestoreRejectsLinksAndEscapingPaths(t *testing.T) {
	for _, header := range []tar.Header{
		{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0600},
		{Name: "workspaces/redirect", Typeflag: tar.TypeSymlink, Linkname: "../../outside"},
	} {
		dir := t.TempDir()
		archive := filepath.Join(dir, "invalid.tar.gz")
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		w := tar.NewWriter(gz)
		if err := w.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		for _, close := range []func() error{w.Close, gz.Close, f.Close} {
			if err := close(); err != nil {
				t.Fatal(err)
			}
		}
		destination := filepath.Join(dir, "restored")
		if err := restoreBackup(t.Context(), archive, destination); err == nil {
			t.Fatalf("restore accepted %q", header.Name)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatalf("failed restore left a destination: %v", err)
		}
	}
}

func TestRestoreClearsLegacyMobileAccess(t *testing.T) {
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := localconfig.EnsureToken(state); err != nil {
		t.Fatal(err)
	}
	if _, err := localconfig.EnsureIdentity(state); err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(state, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, invite.Code, "Fixture iPhone")
	if err != nil {
		t.Fatal(err)
	}
	unused, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Write an archive from an unsanitized database to cover older exports.
	path := filepath.Join(root, "legacy.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	archive := &backupWriter{tar: tar.NewWriter(gz)}
	for _, name := range backupStateFiles {
		if err := archive.add(ctx, filepath.Join(state, name), "state/"+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(archive.tar.Close(), gz.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "restored")
	if err := restoreBackup(ctx, path, dst); err != nil {
		t.Fatal(err)
	}
	restored, err := tasks.OpenStore(ctx, filepath.Join(dst, "state", "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.MobileClientID(ctx, phone.Token); !errors.Is(err, tasks.ErrNotFound) {
		t.Fatalf("restored a phone credential: %v", err)
	}
	if _, err := restored.PairMobileClient(ctx, unused.Code, "Other phone"); !errors.Is(err, tasks.ErrNotFound) {
		t.Fatalf("restored a pending invitation: %v", err)
	}
}
