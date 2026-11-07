package tasks

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPoolJobsClaimedOnceAndRespectEditPermissions(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	read, err := store.EnrollDevice(ctx, "Reader")
	if err != nil {
		t.Fatal(err)
	}
	edit, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	standby, err := store.EnrollDevice(ctx, "Spare")
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range []DeviceCredential{read, edit} {
		if got, err := store.ReserveDeviceWork(ctx, device.ID, true, device.ID == edit.ID); err != nil || got != nil {
			t.Fatalf("registering %s: %+v, %v", device.Name, got, err)
		}
	}
	chat := DefaultConversationID
	write, err := store.QueueVoiceHandoff(ctx, chat, "call", "write", Handoff{
		DeviceID: DevicePool, Title: "Build server", Brief: "Create a standalone Go HTTP server.",
	})
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := store.QueueVoiceHandoff(ctx, chat, "call", "read", Handoff{
		DeviceID: DevicePool, Title: "Inspect fixture", Brief: "Read the version.", ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("local worker claimed pool work: %v", err)
	}
	if got, err := store.ReserveDeviceJob(ctx, standby.ID); got != nil || err != nil {
		t.Fatalf("device without opt-in claimed a pool job: %+v, %v", got, err)
	}
	var editLease string
	for _, claim := range []struct {
		id, expected string
		edits        bool
	}{{read.ID, inspect.ID, false}, {edit.ID, write.ID, true}} {
		got, err := store.ReserveDeviceWork(ctx, claim.id, true, claim.edits)
		if err != nil || got == nil || got.ID != claim.expected || got.Agent.AllowEdits != claim.edits {
			t.Fatalf("ineligible or incorrect claim: %+v, %v", got, err)
		}
		if claim.edits {
			editLease = got.Lease
		}
	}
	if got, err := store.ReserveDeviceWork(ctx, standby.ID, true, true); got != nil || err != nil {
		t.Fatalf("an already claimed job was offered twice: %+v, %v", got, err)
	}
	detail, err := store.WorkDetails(ctx, chat, write.ID)
	if err != nil || detail.Device != "Studio" || detail.Status != Running {
		t.Fatalf("work did not show the assigned machine: %+v, %v", detail, err)
	}
	if err := store.FinishDeviceJob(ctx, edit.ID, write.ID, DeviceResult{
		Lease: editLease, Status: Succeeded, Reply: "Server built.",
	}); err != nil {
		t.Fatal(err)
	}
	raceJob, err := store.QueueVoiceHandoff(ctx, chat, "call", "race", Handoff{
		DeviceID: DevicePool, Title: "Run build", Brief: "Build a portable project.",
	})
	if err != nil {
		t.Fatal(err)
	}
	type claim struct {
		job *DeviceJob
		err error
	}
	claims := make(chan claim, 2)
	for _, id := range []string{edit.ID, standby.ID} {
		go func() {
			job, err := store.ReserveDeviceWork(ctx, id, true, true)
			claims <- claim{job, err}
		}()
	}
	got := 0
	for range 2 {
		result := <-claims
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.job != nil {
			got++
			if result.job.ID != raceJob.ID {
				t.Fatalf("unexpected job: %s", result.job.ID)
			}
		}
	}
	if got != 1 {
		t.Fatalf("shared job was claimed %d times", got)
	}
}
