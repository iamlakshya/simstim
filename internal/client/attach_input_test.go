package client

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/keyread"
)

type heldCancelReader struct {
	cancels      bool
	ignoreCancel bool
	entered      chan struct{}
	release      chan struct{}

	mu      sync.Mutex
	reading bool
	closed  bool
	under   bool
}

func newHeldCancelReader(cancels bool) *heldCancelReader {
	return &heldCancelReader{
		cancels: cancels,
		entered: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
}

func (h *heldCancelReader) Read([]byte) (int, error) {
	h.mu.Lock()
	h.reading = true
	h.mu.Unlock()
	h.entered <- struct{}{}
	<-h.release
	h.mu.Lock()
	h.reading = false
	h.mu.Unlock()
	return 0, io.EOF
}

func (h *heldCancelReader) Cancel() bool {
	if h.cancels {
		if !h.ignoreCancel {
			h.letGo()
		}
		return true
	}
	return false
}

func (h *heldCancelReader) Close() error {
	h.mu.Lock()
	h.closed = true
	h.under = h.reading
	h.mu.Unlock()
	return nil
}

func (h *heldCancelReader) letGo() {
	select {
	case <-h.release:
	default:
		close(h.release)
	}
}

// When Cancel returns false while Read is blocked, finish reports stuck = true,
// leaves the reader unclosed, blocks both a second startAttachInput and
// keyread.For (even when already cached) on the same file, and cleans up the
// old reader once Read finally unblocks.
func TestAttachInputTracksStuckReaderUntilReadExits(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	keyReads := 0
	oldKeyOpen := keyread.Open
	keyread.Open = func(io.Reader) (cancelreader.CancelReader, error) {
		return &stepReader{
			readFn: func(p []byte) (int, error) {
				keyReads++
				p[0] = 'y'
				return 1, nil
			},
			closeFn: func() {},
		}, nil
	}
	defer func() {
		_ = keyread.Release(f)
		keyread.Open = oldKeyOpen
	}()

	// Pre-cache keyread.For(f) as an earlier setup prompt would have done.
	kr, err := keyread.For(f)
	if err != nil {
		t.Fatal(err)
	}

	first := newHeldCancelReader(false)
	defer first.letGo()
	second := newHeldCancelReader(true)
	defer second.letGo()
	opens := 0
	defer SetAttachReader(func(io.Reader) (cancelreader.CancelReader, error) {
		opens++
		if opens == 1 {
			return first, nil
		}
		return second, nil
	})()

	ch := make(chan []byte, 4)
	ai, err := startAttachInput(f, ch, nil)
	if err != nil {
		t.Fatal(err)
	}
	ai.ask()
	<-first.entered

	if !ai.finish() {
		t.Fatal("finish reported stuck = false while Read remained blocked")
	}
	first.mu.Lock()
	closedWhileBlocked := first.closed
	first.mu.Unlock()
	if closedWhileBlocked {
		t.Fatal("finish closed the reader while Read was still running")
	}

	var ue *UserError
	_, err = startAttachInput(f, make(chan []byte, 4), nil)
	if !errors.Is(err, ErrAttachReaderStuck) || !errors.As(err, &ue) || ue.Code != "terminal_busy" {
		t.Fatalf("second startAttachInput on stuck file = %v, want ErrAttachReaderStuck", err)
	}
	if opens != 1 {
		t.Fatalf("opened %d readers while first remained stuck, want 1", opens)
	}
	if _, err := keyread.For(f); !errors.Is(err, keyread.ErrStuck) || !errors.Is(err, keyread.ErrCannotCancel) {
		t.Fatalf("cached keyread.For(f) while attach stuck = %v, want ErrStuck + ErrCannotCancel", err)
	}
	if _, err := kr.ReadKey(context.Background()); !errors.Is(err, keyread.ErrStuck) {
		t.Fatalf("kr.ReadKey while attach stuck = %v, want ErrStuck", err)
	}
	if keyReads != 0 {
		t.Fatalf("keyread read %d times while attach remained stuck, want 0", keyReads)
	}

	// Once the abandoned Read finally returns, the next check closes the old
	// reader cleanly and both keyread and attach can use f again.
	first.letGo()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if err := checkAttachInput(f); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stuck reader never cleared after Read returned")
		}
	}
	first.mu.Lock()
	closedAfter, under := first.closed, first.under
	first.mu.Unlock()
	if !closedAfter || under {
		t.Fatalf("old reader after unblock: closed=%v under=%v", closedAfter, under)
	}

	kr2, err := keyread.For(f)
	if err != nil || kr2 != kr {
		t.Fatalf("keyread.For(f) after unblock = (%v, %v), want cached reader", kr2, err)
	}
	if k, err := kr2.ReadKey(context.Background()); err != nil || k.Rune != 'y' {
		t.Fatalf("kr2.ReadKey after unblock = (%+v, %v), want y", k, err)
	}

	ai2, err := startAttachInput(f, make(chan []byte, 4), nil)
	if err != nil {
		t.Fatalf("startAttachInput after unblock: %v", err)
	}
	ai2.ask()
	<-second.entered
	if ai2.finish() {
		t.Fatal("cancellable second reader reported stuck")
	}
}

// When Cancel returns true yet Read stays blocked past SetAttachReleaseWait,
// finish takes the timeout branch: it reports stuck = true, does not close cr
// under the active Read, blocks both startAttachInput and keyread.For, and
// closes cr once Read finally exits.
func TestAttachInputTimeoutWhenCancelReturnsTrue(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer func() { _ = keyread.Release(f) }()
	defer SetAttachReleaseWait(15 * time.Millisecond)()

	slow := newHeldCancelReader(true)
	slow.ignoreCancel = true
	defer slow.letGo()
	clean := newHeldCancelReader(true)
	defer clean.letGo()

	opens := 0
	defer SetAttachReader(func(io.Reader) (cancelreader.CancelReader, error) {
		opens++
		if opens == 1 {
			return slow, nil
		}
		return clean, nil
	})()

	ai, err := startAttachInput(f, make(chan []byte, 4), nil)
	if err != nil {
		t.Fatal(err)
	}
	ai.ask()
	<-slow.entered

	if !ai.finish() {
		t.Fatal("finish reported stuck = false when Read ignored Cancel past release wait")
	}
	slow.mu.Lock()
	closedWhileBlocked := slow.closed
	slow.mu.Unlock()
	if closedWhileBlocked {
		t.Fatal("finish closed cr while Read was still blocked after Cancel timeout")
	}

	var ue *UserError
	if _, err := startAttachInput(f, make(chan []byte, 4), nil); !errors.Is(err, ErrAttachReaderStuck) || !errors.As(err, &ue) || ue.Code != "terminal_busy" {
		t.Fatalf("second startAttachInput after Cancel timeout = %v, want terminal_busy", err)
	}
	if _, err := keyread.For(f); !errors.Is(err, keyread.ErrStuck) || !errors.Is(err, keyread.ErrReadGoesOn) {
		t.Fatalf("keyread.For after Cancel timeout = %v, want ErrStuck + ErrReadGoesOn", err)
	}

	slow.letGo()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if err := checkAttachInput(f); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed-out reader never cleared after Read exited")
		}
	}
	slow.mu.Lock()
	closedAfter, under := slow.closed, slow.under
	slow.mu.Unlock()
	if !closedAfter || under {
		t.Fatalf("timed-out reader after exit: closed=%v under=%v", closedAfter, under)
	}

	ai2, err := startAttachInput(f, make(chan []byte, 4), nil)
	if err != nil {
		t.Fatalf("startAttachInput after timed-out reader exited: %v", err)
	}
	ai2.ask()
	<-clean.entered
	if ai2.finish() {
		t.Fatal("clean second reader reported stuck")
	}
}

// Demand-driven reading starts no second Read until ask() is called, so a
// reader parked between reads finishes cleanly even when Cancel returns false.
func TestAttachInputDemandDrivenReadStopsBetweenReads(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	reads := 0
	var closed bool
	cr := &stepReader{
		readFn: func(p []byte) (int, error) {
			reads++
			p[0] = 'a'
			return 1, nil
		},
		closeFn: func() { closed = true },
	}
	defer SetAttachReader(func(io.Reader) (cancelreader.CancelReader, error) { return cr, nil })()

	ch := make(chan []byte, 4)
	ai, err := startAttachInput(f, ch, nil)
	if err != nil {
		t.Fatal(err)
	}
	ai.ask()
	if got := <-ch; string(got) != "a" {
		t.Fatalf("got %q, want a", got)
	}
	time.Sleep(10 * time.Millisecond)
	if reads != 1 {
		t.Fatalf("reader ran %d reads before ask(), want 1", reads)
	}
	if ai.finish() {
		t.Fatal("parked reader reported stuck")
	}
	if !closed {
		t.Fatal("parked reader was not closed")
	}
}

type stepReader struct {
	readFn  func([]byte) (int, error)
	closeFn func()
}

func (s *stepReader) Read(p []byte) (int, error) { return s.readFn(p) }
func (s *stepReader) Cancel() bool               { return false }
func (s *stepReader) Close() error               { s.closeFn(); return nil }
