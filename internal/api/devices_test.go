package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestDesktopPairingExportsOnlyWorkerAccess(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := startTestServer(t, dir, Handler(token, store, tasks.NewWorker(store, nil, nil), nil))
	defer server.Close()
	owner := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer owner.Close()
	exportDir := filepath.Join(dir, "downloads")
	if err := os.Mkdir(exportDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(exportDir, "studio.json")
	id, err := owner.CreateWorkerSetup(ctx, "Studio", server.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := localconfig.ReadConnection(path)
	if err != nil || id == "" || profile.DeviceID != id || profile.Token == token {
		t.Fatalf("invalid or overprivileged setup file: %v", err)
	}
	worker := NewConnectionClient(profile)
	defer worker.Close()
	if _, err := worker.Directory(ctx); err == nil {
		t.Fatal("worker obtained owner access")
	}
	if _, err := worker.ClaimDeviceJob(ctx, profile.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := owner.PairDevice(ctx, "Other Mac", server.URL, path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("did not refuse to overwrite the setup file: %v", err)
	}
	devices, err := owner.Devices(ctx)
	if err != nil || len(devices) != 1 || !devices[0].Seen || !devices[0].Online {
		t.Fatalf("device didn't come online or export rollback failed: %+v (%v)", devices, err)
	}
}
