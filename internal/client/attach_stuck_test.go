//go:build !windows

package client_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/client"
)

type nonCancellingTTYReader struct {
	entered chan struct{}
	release chan struct{}

	mu      sync.Mutex
	reading bool
	closed  bool
	under   bool
}

func (r *nonCancellingTTYReader) Read([]byte) (int, error) {
	r.mu.Lock()
	r.reading = true
	r.mu.Unlock()
	r.entered <- struct{}{}
	<-r.release
	r.mu.Lock()
	r.reading = false
	r.mu.Unlock()
	return 0, io.EOF
}

func (r *nonCancellingTTYReader) Cancel() bool { return false }

func (r *nonCancellingTTYReader) Close() error {
	r.mu.Lock()
	r.closed = true
	r.under = r.reading
	r.mu.Unlock()
	return nil
}

// When Attach ends while its terminal reader cannot be cancelled, Attach
// reports ReaderStuck, leaves the reader unclosed rather than race with Read,
// and refuses a second Attach on the same file until the blocked Read exits.
func TestAttachReportsStuckReaderAndBlocksSecondAttach(t *testing.T) {
	r := newAttachRun(t)
	held := &nonCancellingTTYReader{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-held.release:
		default:
			close(held.release)
		}
	}()

	opens := 0
	restore := client.SetAttachReader(func(in io.Reader) (cancelreader.CancelReader, error) {
		opens++
		if opens == 1 {
			return held, nil
		}
		return cancelreader.NewReader(in)
	})
	defer restore()

	id1 := r.session("printf 'READY'; exec sleep 60")
	ctx, cancel := context.WithCancel(context.Background())
	done := r.attach(ctx, id1, client.DefaultSettings())
	r.term.WaitFor("READY")
	<-held.entered
	cancel()

	e := r.end(done)
	if e.err != nil {
		t.Fatalf("first attach error = %v", e.err)
	}
	if !e.res.ReaderStuck {
		t.Fatal("first attach returned ReaderStuck = false while Read remained blocked")
	}
	held.mu.Lock()
	closedWhileBlocked := held.closed
	held.mu.Unlock()
	if closedWhileBlocked {
		t.Fatal("Attach closed the reader while Read was still blocked")
	}

	// A second Attach on the same terminal refuses to start another reader.
	_, err := client.Attach(context.Background(), client.AttachOptions{
		Host: r.ch, Session: id1, Settings: client.DefaultSettings(), In: r.term.TTY, Out: r.term.TTY,
	})
	if !errors.Is(err, client.ErrAttachReaderStuck) {
		t.Fatalf("second attach on stuck terminal = %v, want ErrAttachReaderStuck", err)
	}
	if opens != 1 {
		t.Fatalf("opened %d readers while first remained blocked, want 1", opens)
	}

	// Once the stuck Read unblocks, the next Attach closes the old reader and
	// attaches cleanly.
	close(held.release)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		r.term.Clear()
		ctx2, cancel2 := context.WithCancel(context.Background())
		done2 := r.attach(ctx2, id1, client.DefaultSettings())
		// Wait until either Attach fails fast (if readDone has not closed yet)
		// or enters raw mode cleanly.
		var early *attachEnd
		for !strings.Contains(string(r.term.Output()), rawReady) {
			select {
			case e2 := <-done2:
				early = &e2
			default:
				if time.Now().After(deadline) {
					cancel2()
					<-done2
					t.Fatal("second attach never entered raw mode after release")
				}
				time.Sleep(2 * time.Millisecond)
			}
			if early != nil {
				break
			}
		}
		if early != nil {
			cancel2()
			if errors.Is(early.err, client.ErrAttachReaderStuck) && !time.Now().After(deadline) {
				continue
			}
			t.Fatalf("second attach failed early: %+v, %v", early.res, early.err)
		}
		r.term.Type(detach)
		e2 := r.end(done2)
		cancel2()
		if e2.err != nil || !e2.res.Detached || e2.res.ReaderStuck {
			t.Fatalf("attach after unblock = %+v, %v", e2.res, e2.err)
		}
		break
	}
	held.mu.Lock()
	closedAfter, under := held.closed, held.under
	held.mu.Unlock()
	if !closedAfter || under {
		t.Fatalf("old reader after unblock: closed=%v under=%v", closedAfter, under)
	}
}
