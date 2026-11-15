package tasks

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestBrowserBatchReaderFlushesBeforeUpstreamCompletes(t *testing.T) {
	upstream, origin := io.Pipe()
	defer origin.Close()
	ctx, cancel := context.WithCancel(t.Context())
	reader := newBrowserBatchReader(upstream, cancel)
	defer reader.Close()

	prefix := []byte("the rendered prefix")
	go func() { _, _ = origin.Write(prefix) }()
	first := make(chan []byte, 1)
	go func() {
		buf := make([]byte, browserResponseBatch)
		n, _ := reader.Read(buf)
		first <- buf[:n]
	}()
	select {
	case got := <-first:
		if !bytes.Equal(got, prefix) {
			t.Fatalf("first batch = %q, want %q", got, prefix)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting for a batch while the origin is waiting for the page")
	}

	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("closing the reader left the upstream request active")
	}
	if _, err := origin.Write([]byte("later")); err != io.ErrClosedPipe {
		t.Fatalf("the origin could still write after closing the reader: %v", err)
	}
}

func TestBrowserBatchReaderPreservesSplitChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("response payload"), 40_000)
	_, cancel := context.WithCancel(t.Context())
	reader := newBrowserBatchReader(io.NopCloser(bytes.NewReader(payload)), cancel)
	defer reader.Close()

	var got bytes.Buffer
	if _, err := io.CopyBuffer(writeOnly{&got}, reader, make([]byte, 15_329)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("read %d bytes, want %d identical bytes", got.Len(), len(payload))
	}
}

type writeOnly struct {
	io.Writer
}
