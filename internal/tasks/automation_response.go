package tasks

import (
	"context"
	"io"
	"sync"
	"time"
)

// A batch waits briefly for bytes that are already arriving. Keeping the
// upstream read separate lets the browser receive a flushed HTML prefix even
// when the origin needs the page to execute it before it sends more.
const browserResponseBatchDelay = 10 * time.Millisecond

type browserResponseChunk struct {
	data []byte
	err  error
}

type browserBatchReader struct {
	source   io.ReadCloser
	cancel   context.CancelFunc
	chunks   chan browserResponseChunk
	consumed chan struct{}
	done     chan struct{}
	close    sync.Once
	closeErr error
	pending  browserResponseChunk
}

func newBrowserBatchReader(source io.ReadCloser, cancel context.CancelFunc) *browserBatchReader {
	reader := &browserBatchReader{
		source: source, cancel: cancel, chunks: make(chan browserResponseChunk),
		consumed: make(chan struct{}), done: make(chan struct{}),
	}
	go reader.readUpstream()
	return reader
}

func (r *browserBatchReader) readUpstream() {
	defer close(r.chunks)
	// The handoff lets the reader reuse this buffer after the consumer has
	// copied it. There is at most one outstanding chunk.
	buffer := make([]byte, 32<<10)
	for {
		n, err := r.source.Read(buffer)
		if n == 0 && err == nil {
			continue
		}
		select {
		case r.chunks <- browserResponseChunk{buffer[:n], err}:
		case <-r.done:
			return
		}
		select {
		case <-r.consumed:
		case <-r.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (r *browserBatchReader) Read(p []byte) (int, error) {
	p = p[:min(len(p), browserResponseBatch)]
	var written int
	var timer *time.Timer
	var timeout <-chan time.Time
	var deadline time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for written < len(p) {
		if len(r.pending.data) == 0 && r.pending.err == nil {
			select {
			case chunk, ok := <-r.chunks:
				if !ok {
					return written, io.EOF
				}
				r.pending = chunk
			case <-timeout:
				return written, nil
			case <-r.done:
				return written, io.ErrClosedPipe
			}
		}
		copied := copy(p[written:], r.pending.data)
		written += copied
		r.pending.data = r.pending.data[copied:]
		if len(r.pending.data) == 0 {
			err := r.pending.err
			r.pending = browserResponseChunk{}
			select {
			case r.consumed <- struct{}{}:
			case <-r.done:
				return written, io.ErrClosedPipe
			}
			if err != nil {
				return written, err
			}
		}
		if timer == nil && written < len(p) {
			deadline = time.Now().Add(browserResponseBatchDelay)
			timer = time.NewTimer(browserResponseBatchDelay)
			timeout = timer.C
		} else if timer != nil && !time.Now().Before(deadline) {
			return written, nil
		}
	}
	return written, nil
}

func (r *browserBatchReader) Close() error {
	r.close.Do(func() {
		close(r.done)
		r.cancel()
		r.closeErr = r.source.Close()
	})
	return r.closeErr
}
