package keys

import (
	"context"
	"fmt"
	"io"
)

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
// waits for the read to end, so that the caller may close r: a read that
// still ran would use r as it closes. When Cancel reports that it could
// not, as on some Windows consoles, ReadUntil returns at once, and the
// read stays waiting in its own goroutine until the process ends: the
// caller must not close r then.
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
			<-exited
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
