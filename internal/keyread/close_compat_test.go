package keyread

import (
	"context"
	"io"
	"testing"

	"github.com/muesli/cancelreader"
)

// C5's lone-ESC shutdown property uses the canonical Release owner. It
// cancels the read and joins its goroutine before the terminal may close.
func TestCloseStopsReadInFlight(t *testing.T) {
	src := newFake()
	src.entered = make(chan struct{}, 16)
	oldOpen := Open
	Open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
	defer func() { Open = oldOpen }()
	f := tempFile(t)
	r, err := For(f)
	if err != nil {
		t.Fatal(err)
	}
	clk := newTimeline()
	r.after, r.now = clk.after, clk.now
	got := make(chan Key, 1)
	go func() {
		k, err := r.ReadKey(context.Background())
		if err != nil {
			t.Error(err)
		}
		got <- k
	}()
	src.write("\x1b")
	if !started(src, 2) {
		t.Fatal("the read after the ESC never started")
	}
	clk.set(EscWait)
	if k := first(t, got, "a lone ESC"); k.Kind != Esc {
		t.Fatalf("a lone ESC = %+v", k)
	}
	r.mu.Lock()
	done := r.readDone
	r.mu.Unlock()
	if err := Release(f); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	cancelled := src.canceled
	src.mu.Unlock()
	if !cancelled {
		t.Fatal("Release did not cancel the reader")
	}
	select {
	case <-done:
	default:
		t.Fatal("Release returned before the read goroutine stopped")
	}
}
