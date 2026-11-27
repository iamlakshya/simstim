package keys

import (
	"context"
	"fmt"
	"io"
	"time"
)

const readReleaseWait = 5 * time.Second

var afterReadReleaseWait = time.After

// CancelReader is a reader whose waiting read can be asked to stop, as the
// readers of github.com/muesli/cancelreader can. Cancel reports whether it
// could.
type CancelReader interface {
	io.Reader
	Cancel() bool
}

// ReadUntil reads r, and passes each chunk to fn, until fn returns false or
// a read fails, which end it with nil, or until ctx ends, which ends it
// with ctx's error. It starts a read only when fn has asked for more, so
// it takes nothing past the chunk where fn stopped. When it ends it asks r
// to cancel a read that waits. When Cancel reports that it could, ReadUntil
// waits at most five seconds for the read to end. Cancel can report success
// without waking a read on some Windows consoles. The caller must track the
// read's completion before closing r, even when Cancel reports success.
// When Cancel reports that it could not stop the read, ReadUntil returns at
// once. A read that still waits keeps its own goroutine until it ends.
func ReadUntil(ctx context.Context, r CancelReader, fn func([]byte) bool) error {
	type chunk struct {
		b   []byte
		err error
	}
	chunks := make(chan chunk)
	more := make(chan struct{})
	done := make(chan struct{})
	exited := make(chan struct{})
	defer func() {
		close(done)
		if r.Cancel() {
			select {
			case <-exited:
			case <-afterReadReleaseWait(readReleaseWait):
			}
		}
	}()
	go func() {
		defer close(exited)
		for {
			buf := make([]byte, 4096)
			n, err := r.Read(buf)
			select {
			case chunks <- chunk{buf[:n], err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
			select {
			case <-more:
			case <-done:
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("stop the read: %w", ctx.Err())
		case c := <-chunks:
			if len(c.b) > 0 && !fn(c.b) {
				return nil
			}
			if c.err != nil {
				return nil
			}
			more <- struct{}{}
		}
	}
}
