package keyread

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
)

type joiningReader struct {
	started  chan struct{}
	wake     chan struct{}
	once     sync.Once
	exited   atomic.Bool
	badClose atomic.Bool
	closed   atomic.Int32
}

func (r *joiningReader) Read([]byte) (int, error) {
	defer r.exited.Store(true)
	close(r.started)
	<-r.wake
	return 0, cancelreader.ErrCanceled
}

func (r *joiningReader) Cancel() bool {
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.once.Do(func() { close(r.wake) })
	}()
	return true
}

func (r *joiningReader) Close() error {
	if !r.exited.Load() {
		r.badClose.Store(true)
	}
	r.closed.Add(1)
	r.once.Do(func() { close(r.wake) })
	return nil
}

func TestCloseForWaitsForReadAndEvictsEntry(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	src := &joiningReader{started: make(chan struct{}), wake: make(chan struct{})}
	old := open
	open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
	t.Cleanup(func() { open = old; CloseFor(f); _ = f.Close() })
	r, err := For(f)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := r.ReadKey(ctx); returned <- err }()
	<-src.started
	cancel()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	CloseFor(f)
	if !src.exited.Load() || src.badClose.Load() {
		t.Error("CloseFor closed its source before the in-flight read exited")
	}
	readersMu.Lock()
	_, exists := readers[f]
	readersMu.Unlock()
	if exists {
		t.Error("CloseFor retained the file in its reader registry")
	}
	CloseFor(f)
	if n := src.closed.Load(); n != 1 {
		t.Errorf("closed source %d times, want once", n)
	}
}
