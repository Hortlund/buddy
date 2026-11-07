package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func defaultWorkspaceRoot(ctx context.Context, store *tasks.Store, config, dataDir string) (string, error) {
	config, err := filepath.EvalSymlinks(config)
	if err != nil {
		return "", err
	}
	namespace := sha256.Sum256([]byte(dataDir))
	name := fmt.Sprintf("%x", namespace[:8])
	parent := filepath.Join(config, "buddy-workspaces")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("buddy-workspaces must be a directory")
	}
	parent, err = localconfig.PrivateDir(parent)
	if err != nil {
		return "", err
	}
	current := filepath.Join(parent, name)
	legacyParent := filepath.Join(config, "Buddy Workspaces")
	if canonical, err := filepath.EvalSymlinks(legacyParent); err == nil {
		legacyParent = canonical
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	legacy := filepath.Join(legacyParent, name)
	if legacy == current {
		return current, nil
	}

	oldInfo, oldErr := os.Lstat(legacy)
	newInfo, newErr := os.Lstat(current)
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return "", oldErr
	}
	if newErr != nil && !errors.Is(newErr, os.ErrNotExist) {
		return "", newErr
	}
	if oldErr == nil {
		if newErr == nil {
			return "", errors.New("both the old and new managed workspace folders exist")
		}
		if !oldInfo.IsDir() {
			return "", errors.New("the old managed workspace is not a directory")
		}
		legacy, err = localconfig.PrivateDir(legacy)
		if err != nil {
			return "", err
		}
		if err := os.Rename(legacy, current); err != nil {
			return "", err
		}
	} else if newErr != nil {
		return current, nil
	} else if !newInfo.IsDir() {
		return "", errors.New("the new managed workspace is not a directory")
	}

	// The old directory can already be gone if the daemon stopped after
	// the rename. Reapplying the database rewrite is safe on every startup.
	current, err = localconfig.PrivateDir(current)
	if err != nil {
		return "", err
	}
	if err := store.RelocateManagedWorkspaces(ctx, legacy, current); err != nil {
		return "", err
	}
	return current, nil
}
