package egress

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// The server owns the original body. Transport and decoding wrappers must
// not use HTTP/1 Body.Close to drain an unfinished upload from the guest.
type httpBodyProgress struct {
	reader    io.Reader
	remaining int64
	complete  atomic.Bool
	closeRead func()
	closeOnce sync.Once
	mu        sync.Mutex
	stopped   bool
	reads     sync.WaitGroup
}

func (b *httpBodyProgress) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return 0, net.ErrClosed
	}
	b.reads.Add(1)
	b.mu.Unlock()
	defer b.reads.Done()
	n, err := b.reader.Read(p)
	if b.remaining > 0 {
		b.remaining -= int64(n)
		if b.remaining == 0 {
			b.complete.Store(true)
		}
	}
	if err == io.EOF {
		b.complete.Store(true)
	}
	return n, err //nolint:wrapcheck // preserve the server body read result
}

func (b *httpBodyProgress) Close() error {
	b.stop()
	b.closeOnce.Do(func() {
		if b.closeRead != nil && !b.complete.Load() {
			b.closeRead()
		}
	})
	return nil
}

func (b *httpBodyProgress) stop() {
	b.mu.Lock()
	b.stopped = true
	b.mu.Unlock()
}

func (b *httpBodyProgress) wait() { b.reads.Wait() }
