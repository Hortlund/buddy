package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"buddy/internal/tasks"
)

func TestManagedWorkspaceRenameKeepsFilesAndQueuedWork(t *testing.T) {
	ctx := t.Context()
	config, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	store, err := tasks.OpenStore(ctx, filepath.Join(data, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	namespace := sha256.Sum256([]byte(data))
	legacy := filepath.Join(config, "Buddy Workspaces", fmt.Sprintf("%x", namespace[:8]))
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableWorkspaces(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	profile, err := store.Agent(ctx, tasks.DefaultAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.Workspace, "notes.txt"), []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.Send(ctx, tasks.DefaultConversationID, "Read my notes", "migrate")
	if err != nil {
		t.Fatal(err)
	}
	custom, err := store.CreateAgent(ctx, tasks.Agent{Name: "Custom"})
	if err != nil {
		t.Fatal(err)
	}
	custom.Workspace = t.TempDir()
	custom, err = store.SaveAgent(ctx, custom.ID, custom)
	if err != nil {
		t.Fatal(err)
	}

	current, err := defaultWorkspaceRoot(ctx, store, config, data)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(current)) != "buddy-workspaces" {
		t.Fatalf("unexpected workspace root %q", current)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("old namespace still exists: %v", err)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(current, profile.ID))
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(expected, "notes.txt"))
	if err != nil || string(contents) != "keep me" {
		t.Fatalf("workspace file after migration = %q, %v", contents, err)
	}
	profile, err = store.Agent(ctx, profile.ID)
	if err != nil || profile.Workspace != expected || !profile.AllowEdits {
		t.Fatalf("profile after migration = %+v, %v", profile, err)
	}
	job, err := store.Get(ctx, queued.ID)
	if err != nil || job.Repository != expected || job.Status != tasks.Queued {
		t.Fatalf("queued job after migration = %+v, %v", job, err)
	}
	saved, err := store.Agent(ctx, custom.ID)
	if err != nil || saved.Workspace != custom.Workspace {
		t.Fatalf("custom workspace after migration = %+v, %v", saved, err)
	}
	again, err := defaultWorkspaceRoot(ctx, store, config, data)
	if err != nil || again != current {
		t.Fatalf("migration on second startup = %q, %v", again, err)
	}
}

func TestMovingHomeRebasesQueuedWorkAfterFilesArrive(t *testing.T) {
	ctx := t.Context()
	oldRoot, newRoot := t.TempDir(), t.TempDir()
	store, err := tasks.OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, oldRoot); err != nil {
		t.Fatal(err)
	}
	job, _, err := store.Send(ctx, tasks.DefaultConversationID, "Read notes", "")
	if err != nil {
		t.Fatal(err)
	}
	repoSuffix := filepath.Join(tasks.DefaultAgentID, "repo")
	oldRepo := filepath.Join(oldRoot, repoSuffix)
	if err := os.Mkdir(oldRepo, 0700); err != nil {
		t.Fatal(err)
	}
	check, _, err := store.Create(ctx, tasks.Request{Kind: tasks.RepoCheck, Repository: oldRepo, TimeoutSeconds: 60}, "")
	if err != nil {
		t.Fatal(err)
	}
	newRoot, err = filepath.EvalSymlinks(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnableWorkspaces(ctx, newRoot); err == nil {
		t.Fatal("repointed the home before its workspace files arrived")
	}
	expected := filepath.Join(newRoot, tasks.DefaultAgentID)
	if err := os.Rename(filepath.Join(oldRoot, tasks.DefaultAgentID), expected); err != nil {
		t.Fatal(err)
	}
	// A queued repo check can target a nested folder absent from agent settings.
	// Leave that folder behind first and verify that migration waits for it.
	newRepo := filepath.Join(newRoot, repoSuffix)
	if err := os.Rename(newRepo, filepath.Join(oldRoot, "repo-held-back")); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableWorkspaces(ctx, newRoot); err == nil {
		t.Fatal("repointed the home before the queued check's repository arrived")
	}
	if err := os.Rename(filepath.Join(oldRoot, "repo-held-back"), newRepo); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableWorkspaces(ctx, newRoot); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Get(ctx, job.ID)
	if err != nil || saved.Repository != expected {
		t.Fatalf("queued workspace = %q, %v", saved.Repository, err)
	}
	saved, err = store.Get(ctx, check.ID)
	if err != nil || saved.Repository != newRepo || saved.Status != tasks.Queued {
		t.Fatalf("queued repository check = %+v, %v", saved, err)
	}
}
