package api

import (
	"bytes"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestAttachmentsPersistWithoutReturningPayloadsInChat(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "buddy.db")
	store, err := tasks.OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := startTestServer(t, dir, Handler(token, store, tasks.NewWorker(store, nil, nil), nil))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()

	id := tasks.DefaultConversationID
	files := []tasks.Attachment{{Name: "report.txt", Data: []byte("test report")}, {Name: "empty.txt"}}
	task, err := client.SendWithAttachments(ctx, id, "", "", "upload", files)
	if err != nil {
		t.Fatal(err)
	}
	same, err := client.SendWithAttachments(ctx, id, "", "", "upload", files)
	if err != nil || same.ID != task.ID {
		t.Fatalf("retry did not reuse the task: %v", err)
	}
	_, err = client.SendWithAttachments(ctx, id, "", "", "upload", []tasks.Attachment{{Name: "report.txt", Data: []byte("other")}})
	var remote *HTTPError
	if !errors.As(err, &remote) || remote.Status != 409 {
		t.Fatalf("different attachment should conflict: %v", err)
	}
	for _, name := range []string{"../report.txt", "x/y.txt", "x\\y.txt"} {
		if _, err := client.SendWithAttachments(ctx, id, "", "", "", []tasks.Attachment{{Name: name}}); !errors.As(err, &remote) || remote.Status != 400 {
			t.Fatalf("accepted invalid filename %q: %v", name, err)
		}
	}
	chat, err := client.Conversation(ctx, id, 0)
	if err != nil || len(chat.Messages) != 1 || len(chat.Messages[0].Attachments) != 2 {
		t.Fatalf("did not show saved attachment metadata: %v", err)
	}
	first := chat.Messages[0].Attachments[0]
	if first.Name != files[0].Name || first.Size != len(files[0].Data) || len(first.Data) != 0 {
		t.Fatalf("wrong attachment metadata: %+v", first)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := tasks.OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Attachment(ctx, task.ID, 0)
	if err != nil || !bytes.Equal(got.Data, files[0].Data) {
		t.Fatalf("attachment didn't survive restart: %v", err)
	}
}

// Make the transfer outlive the ordinary write deadline. The bulk endpoint
// must extend it before encoding the attachment response.
type delayedAttachmentWriter struct {
	http.ResponseWriter
}

func (w delayedAttachmentWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w delayedAttachmentWriter) Write(data []byte) (int, error) {
	time.Sleep(150 * time.Millisecond)
	return w.ResponseWriter.Write(data)
}

func TestAttachmentDownloadExtendsResponseDeadline(t *testing.T) {
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
	data := bytes.Repeat([]byte{1}, tasks.MaxAttachmentBytes)
	task, _, err := store.SendWithAttachments(ctx, tasks.DefaultConversationID, "", "", "", []tasks.Attachment{{Name: "file.bin", Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(token, store, tasks.NewWorker(store, nil, nil), nil)
	server := startTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			t.Error(err)
			return
		}
		handler.ServeHTTP(delayedAttachmentWriter{w}, r)
	}))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()
	file, err := client.Attachment(ctx, task.ID, 0)
	if err != nil || !bytes.Equal(file.Data, data) {
		t.Fatalf("the full attachment did not survive the response deadline: %v", err)
	}
}
