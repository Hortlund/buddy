package tasks

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttachmentHandoffStagesOnlyTheAssignedFiles(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	device, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	files := []Attachment{{Name: "diagram.png", Data: picture.Bytes()}, {Name: "notes.txt", Data: []byte("read me")}}
	upload, _, err := store.SendWithAttachments(ctx, DefaultConversationID, "", "here are the files", "", files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, upload.ID, Succeeded, "Finished.", "Got them."); err != nil {
		t.Fatal(err)
	}
	cancelled, _, err := store.SendWithAttachments(ctx, DefaultConversationID, "", "", "", []Attachment{{Name: "private.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.cancel(ctx, cancelled.ID, true); err != nil {
		t.Fatal(err)
	}
	parent, _, err := store.Send(ctx, DefaultConversationID, "read the earlier files on Studio", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, parent)
	if err != nil || len(turn.Attachments) != 2 || !turn.earlierAttachments || turn.attachmentSource != upload.ID {
		t.Fatalf("companion missing attachments: %v", err)
	}
	for _, message := range turn.Messages {
		if message.TaskID == cancelled.ID && len(message.Attachments) != 0 {
			t.Fatal("the canceled upload remained in model context")
		}
	}
	plain, _, err := store.Send(ctx, DefaultConversationID, "check the repo on Studio", "")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := saveHandoff(ctx, tx, parent.ID, Handoff{DeviceID: device.ID, Title: "Read files", Brief: "Read the attached files.", UseEarlierAttachments: true}, turn.attachmentSource); err != nil {
		t.Fatal(err)
	}
	if _, err := saveHandoff(ctx, tx, plain.ID, Handoff{DeviceID: device.ID, Title: "Check repo", Brief: "Check the repo."}, turn.attachmentSource); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.ClaimDeviceJob(ctx, device.ID)
	if err != nil || legacy == nil || legacy.Title != "Check repo" || len(legacy.Attachments) != 0 {
		t.Fatalf("older worker did not skip the attachment job: %+v (%v)", legacy, err)
	}
	if err := store.FinishDeviceJob(ctx, device.ID, legacy.ID, DeviceResult{Lease: legacy.Lease, Status: Succeeded}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ReserveDeviceJob(ctx, device.ID)
	if err != nil || job == nil || !job.NeedsStart || len(job.Attachments) != 2 || !bytes.Equal(job.Attachments[1].Data, files[1].Data) {
		t.Fatalf("worker did not receive the assigned files: %v", err)
	}
	if task, err := store.Get(ctx, job.ID); err != nil || task.Status != Queued || task.StartedAt != nil {
		t.Fatalf("execution began before the worker received its payload: %+v (%v)", task, err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE device_runs SET expires=? WHERE task_id=?", time.Now().Add(-time.Second).UnixMicro(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ExpireDeviceJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.StartDeviceJob(ctx, device.ID, job.ID, job.Lease); !errors.Is(err, ErrInactive) {
		t.Fatalf("expired delivery could still start: %v", err)
	}
	retry, err := store.ReserveDeviceJob(ctx, device.ID)
	if err != nil || retry == nil || retry.ID != job.ID || retry.Lease == job.Lease {
		t.Fatalf("undelivered job was not available for retry: %+v (%v)", retry, err)
	}
	for range 2 {
		if err := store.StartDeviceJob(ctx, device.ID, retry.ID, retry.Lease); err != nil {
			t.Fatal(err)
		}
	}
	if task, err := store.Get(ctx, job.ID); err != nil || task.Status != Running || task.StartedAt == nil {
		t.Fatalf("execution did not begin after acknowledgment: %+v (%v)", task, err)
	}

	root, staged, images, err := stageAttachments(t.TempDir(), job.Attachments)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if len(images) != 1 || len(staged) != 2 || staged[1].Preview != "read me" {
		t.Fatalf("incorrect runner inputs: %+v, %v", staged, images)
	}
	flags := sandboxArgs(t.TempDir(), true, true, nil, nil, root)
	if !strings.Contains(strings.Join(flags, " "), tomlString(root)+`="read"`) {
		t.Fatal("device attachment directory was not granted read access")
	}
}

func TestRemovedQueuedUploadNeverBecomesTheNextTurnsAttachment(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	upload, _, err := store.SendWithAttachments(ctx, DefaultConversationID, "", "", "", []Attachment{{Name: "private.txt", Data: []byte("private")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.cancel(ctx, upload.ID, true); err != nil {
		t.Fatal(err)
	}
	next, _, err := store.Send(ctx, DefaultConversationID, "Hello", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.turn(ctx, next)
	if err != nil || len(turn.Attachments) != 0 {
		t.Fatalf("canceled upload was included in the next request: %v", err)
	}
}
