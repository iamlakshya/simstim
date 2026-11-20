package proto

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// held is a writer whose first write waits until the test lets it go.
type held struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	writes  int
	entered chan struct{}
	release chan struct{}
}

func (h *held) Write(p []byte) (int, error) {
	h.mu.Lock()
	h.writes++
	first := h.writes == 1
	h.mu.Unlock()
	if first {
		h.entered <- struct{}{}
		<-h.release
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buf.Write(p)
}

// A frame that waits for its turn behind another gives up at its
// context's end, at once, and writes nothing, also once the turn comes.
func TestAWaitingFrameGivesUpAtItsCancel(t *testing.T) {
	out := &held{entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := NewWriter(out)
	first := make(chan error, 1)
	go func() { first <- w.Write(TPing, nil) }()
	<-out.entered
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { queued <- w.WriteCtx(ctx, TRequest, []byte(`{"op":"doctor.fix"}`)) }()
	cancel()
	select {
	case err := <-queued:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a waiting frame at its cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting frame did not give up at its cancel")
	}
	close(out.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	f, err := Read(bufio.NewReader(bytes.NewReader(out.buf.Bytes())))
	if err != nil || f.Type != TPing || out.buf.Len() != 5 {
		t.Errorf("the writer wrote %q, want the first frame alone", out.buf.Bytes())
	}
}

// When the turn is free and the context has ended, the end wins: a frame
// never goes after its cancel, however the wait comes out.
func TestAFrameAfterItsCancelGoesNowhere(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 200 {
		if err := w.WriteCtx(ctx, TRequest, []byte("x")); !errors.Is(err, context.Canceled) {
			t.Fatalf("a frame after its cancel: %v", err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("the writer wrote %d bytes after the cancel", out.Len())
	}
}
