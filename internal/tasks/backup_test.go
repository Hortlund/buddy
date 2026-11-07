package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestOnlineSnapshotUsesSeparateConnection(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	watch, err := store.CreateWatch(ctx, Watch{Name: "CI", Source: GitHubCI, Scope: "acme/repo",
		ConversationID: DefaultConversationID, Mode: WatchDigest, Budget: 5})
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	path := filepath.Join(dir, "snapshot.db")
	copyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := store.SnapshotOnline(copyCtx, path); err != nil {
		t.Fatalf("snapshot blocked behind the server connection: %v", err)
	}
	copy, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	watches, err := copy.Watches(ctx)
	if err != nil || len(watches) != 1 || watches[0].ID != watch.ID {
		t.Fatalf("snapshot lost committed WAL contents: %+v, %v", watches, err)
	}
}

func TestDatabaseSnapshotsExcludeMobileAccess(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	invite, err := store.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.PairMobileClient(ctx, invite.Code, "Fixture iPhone")
	if err != nil {
		t.Fatal(err)
	}
	for label, snapshot := range map[string]func(context.Context, string) error{
		"offline": store.Snapshot, "online": store.SnapshotOnline,
	} {
		t.Run(label, func(t *testing.T) {
			path := filepath.Join(dir, label+".db")
			if err := snapshot(ctx, path); err != nil {
				t.Fatal(err)
			}
			copy, err := OpenStore(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer copy.Close()
			if _, err := copy.MobileClientID(ctx, phone.Token); !errors.Is(err, ErrNotFound) {
				t.Fatalf("copied phone credential: %v", err)
			}
		})
	}
	if id, err := store.MobileClientID(ctx, phone.Token); err != nil || id != phone.ID {
		t.Fatalf("snapshot changed live credentials: %q, %v", id, err)
	}
}
