package cli

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

	"github.com/iamlakshya/simstim/internal/keyread"
	"github.com/iamlakshya/simstim/internal/keys"
)

type heldKeyReader struct {
	started, release, readEnded chan struct{}
	cancelOK                    bool
	cancels, closes             atomic.Int32
	reading, closedDuringRead   atomic.Bool
	releaseOnce                 sync.Once
}

func newHeldKeyReader(cancelOK bool) *heldKeyReader {
	return &heldKeyReader{
		started: make(chan struct{}), release: make(chan struct{}), readEnded: make(chan struct{}), cancelOK: cancelOK,
	}
}

func (r *heldKeyReader) Read([]byte) (int, error) {
	r.reading.Store(true)
	close(r.started)
	<-r.release
	r.reading.Store(false)
	close(r.readEnded)
	return 0, io.EOF
}

func (r *heldKeyReader) Cancel() bool {
	r.cancels.Add(1)
	return r.cancelOK
}

func (r *heldKeyReader) Close() error {
	r.closes.Add(1)
	if r.reading.Load() {
		r.closedDuringRead.Store(true)
	}
	return nil
}

func (r *heldKeyReader) letGo() { r.releaseOnce.Do(func() { close(r.release) }) }

func TestStoppableOwnsCancellationAndCloseOnce(t *testing.T) {
	for _, cancelOK := range []bool{false, true} {
		t.Run(map[bool]string{false: "cannot-cancel", true: "cancel-claims-success"}[cancelOK], func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "terminal")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			r := newHeldKeyReader(cancelOK)
			s := &stoppable{cr: r}
			done := make(chan struct{})
			go func() { defer close(done); _, _ = s.Read(make([]byte, 1)) }()
			t.Cleanup(func() {
				r.letGo()
				<-done
				_ = keyread.Release(f)
			})
			select {
			case <-r.started:
			case <-done:
				t.Fatal("the reader ended before its input read started")
			case <-time.After(5 * time.Second):
				t.Fatal("the input read never started")
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					if got := s.Cancel(); got != cancelOK {
						t.Errorf("Cancel = %v, want %v", got, cancelOK)
					}
				})
			}
			wg.Wait()
			s.finish(f)
			s.finish(f)
			if got := r.cancels.Load(); got != 1 {
				t.Errorf("underlying Cancel called %d times, want one", got)
			}
			if r.closes.Load() != 0 || r.closedDuringRead.Load() {
				t.Fatal("the reader closed while its read was live")
			}
			cause := keyread.ErrCannotCancel
			if cancelOK {
				cause = keyread.ErrReadGoesOn
			}
			if err := keyread.CheckStuck(f); !errors.Is(err, keyread.ErrStuck) || !errors.Is(err, cause) {
				t.Errorf("CheckStuck = %v, want ErrStuck and %v", err, cause)
			}
			if _, err := s.Read(make([]byte, 1)); !errors.Is(err, cancelreader.ErrCanceled) {
				t.Errorf("Read after cancel = %v, want ErrCanceled", err)
			}
			r.letGo()
			<-done
			if err := keyread.CheckStuck(f); err != nil {
				t.Fatal(err)
			}
			s.finish(f)
			_ = s.Cancel()
			if r.closes.Load() != 1 || r.closedDuringRead.Load() || r.cancels.Load() != 1 {
				t.Errorf("completed reader: closes=%d under-read=%v cancels=%d", r.closes.Load(), r.closedDuringRead.Load(), r.cancels.Load())
			}
		})
	}
}

func TestStoppableCloseWithoutAReadIsIdempotent(t *testing.T) {
	r := newHeldKeyReader(true)
	s := &stoppable{cr: r}
	s.finish(nil)
	s.finish(nil)
	_ = s.Cancel()
	if r.closes.Load() != 1 || r.cancels.Load() != 1 {
		t.Errorf("idle reader: closes=%d cancels=%d, want one each", r.closes.Load(), r.cancels.Load())
	}
}

func TestStoppableReadUntilDoesNotCloseAClaimedCancellation(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	r := newHeldKeyReader(true)
	s := &stoppable{cr: r}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		err := keys.ReadUntil(ctx, s, func([]byte) bool { return true })
		s.finish(f)
		done <- err
	}()
	t.Cleanup(func() {
		r.letGo()
		<-ended
		select {
		case <-r.started:
			<-r.readEnded
		default:
		}
		s.mu.Lock()
		readDone := s.readDone
		s.mu.Unlock()
		if readDone != nil {
			<-readDone
		}
		_ = keyread.Release(f)
	})
	select {
	case <-r.started:
	case <-ended:
		t.Fatal("ReadUntil ended before its input read started")
	case <-time.After(5 * time.Second):
		t.Fatal("the input read never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ReadUntil = %v, want cancellation", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the successful cancellation kept its reader indefinitely")
	}
	if r.closes.Load() != 0 || r.closedDuringRead.Load() {
		t.Fatal("the blocked reader was closed")
	}
	if err := keyread.CheckStuck(f); !errors.Is(err, keyread.ErrReadGoesOn) {
		t.Errorf("CheckStuck = %v, want ErrReadGoesOn", err)
	}
}
