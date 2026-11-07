package tasks

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkInputStatusMigratesExistingTasks(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(schema, "'needs_input',", "", 1)
	_, err = db.ExecContext(ctx, legacy+chatSchema+`
		INSERT INTO tasks(rowid,id,request_key,kind,repo,command,timeout,status,summary,created)
			VALUES (42,'prior','prior','agent_turn','','null',60,'succeeded','Replied.',1);
		INSERT INTO chat_turns VALUES ('prior','conv_buddy','Buddy','',0);
		INSERT INTO messages(task_id,role,text,created) VALUES ('prior','assistant','Earlier reply.',2);
		INSERT INTO events(task_id,at,stream,text) VALUES ('prior',2,'system','Replied.');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chat, err := store.Conversation(ctx, DefaultConversationID, 0)
	if err != nil || len(chat.Messages) != 1 || chat.Messages[0].Text != "Earlier reply." {
		t.Fatalf("existing chat was lost: %+v, %v", chat.Messages, err)
	}
	var rowid int
	if err := store.db.QueryRowContext(ctx, "SELECT rowid FROM tasks WHERE id='prior'").Scan(&rowid); err != nil || rowid != 42 {
		t.Fatalf("existing task order changed: %d, %v", rowid, err)
	}
	if err := addEvent(ctx, store.db, "missing", "system", "Invalid."); err == nil {
		t.Fatal("foreign keys were not restored")
	}
	job, err := store.QueueVoiceWork(ctx, DefaultConversationID, DefaultAgentID, "call", "action", "Review", "Review the report.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimLane(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, job.ID, NeedsInput, "Waiting for your reply.", "Which report?"); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceJobWaitsAcrossRestartAndResumesWithNewLease(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "buddy.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	device, err := store.EnrollDevice(ctx, "Studio")
	if err != nil {
		t.Fatal(err)
	}
	id := DefaultConversationID
	files := []Attachment{{Name: "first.txt", Data: []byte("initial")}}
	parent, _, err := store.SendWithAttachments(ctx, id, "", "Inspect these files", "", files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.claim(ctx); err != nil {
		t.Fatal(err)
	}
	action := &TurnAction{Handoff: &Handoff{DeviceID: device.ID, Title: "Inspect files", Brief: "Compare the input files."}}
	if err := store.finishTurn(ctx, parent.ID, Succeeded, "Replied.", "", action); err != nil {
		t.Fatal(err)
	}
	job, err := store.ReserveDeviceJob(ctx, device.ID)
	if err != nil || job == nil || !job.CanRequestInput || !job.NeedsStart {
		t.Fatalf("first lease: %+v, %v", job, err)
	}
	if err := store.StartDeviceJob(ctx, device.ID, job.ID, job.Lease); err != nil {
		t.Fatal(err)
	}
	const question = "Can you send the second file?"
	report := DeviceResult{Lease: job.Lease, Status: NeedsInput, Reply: question}
	if err := store.FinishDeviceJob(ctx, device.ID, job.ID, report); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDeviceJob(ctx, device.ID, job.ID, report); err != nil {
		t.Fatalf("finish retry: %v", err)
	}
	store.Close()
	store, err = OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	detail, err := store.WorkDetails(ctx, id, job.ID)
	if err != nil || detail.Status != NeedsInput || detail.Question != question || detail.InputID == "" {
		t.Fatalf("waiting job after restart: %+v, %v", detail, err)
	}
	if next, err := store.ReserveDeviceJob(ctx, device.ID); err != nil || next != nil {
		t.Fatalf("waiting job was offered before input: %+v, %v", next, err)
	}
	second := []Attachment{{Name: "second.txt", Data: []byte("more")}}
	if _, err := store.AnswerWorkInput(ctx, "wrong-chat", detail.InputID, "", "answer", second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another chat could answer: %v", err)
	}
	if resumed, err := store.AnswerWorkInput(ctx, id, detail.InputID, "", "answer", second); err != nil || !resumed {
		t.Fatalf("answer: %v, %v", resumed, err)
	}
	if _, err := store.AnswerWorkInput(ctx, id, detail.InputID, "changed", "answer", second); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry changed the answer: %v", err)
	}
	if err := store.FinishDeviceJob(ctx, device.ID, job.ID, report); !errors.Is(err, ErrInactive) {
		t.Fatalf("old lease could report after resumption: %v", err)
	}
	next, err := store.ReserveDeviceJob(ctx, device.ID)
	if err != nil || next == nil || next.ID != job.ID || next.Lease == job.Lease || !next.NeedsStart ||
		!strings.Contains(next.Brief, question) || len(next.Attachments) != 2 ||
		next.Attachments[0].Name != "first.txt" || next.Attachments[1].Name != "second.txt" {
		t.Fatalf("resumed lease lost its context: %+v, %v", next, err)
	}
}
