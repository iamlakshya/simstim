//go:build !windows

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

type demoHeldReader struct {
	input, release chan []byte
	entered        chan struct{}
	left           chan struct{}
	cancelled      chan struct{}
	cancels        bool
	enterOnce      sync.Once
	cancelOnce     sync.Once
	leftOnce       sync.Once

	mu                        sync.Mutex
	reads, cancelsN, closes   int
	reading, closedDuringRead bool
}

func newDemoHeldReader(cancels bool) *demoHeldReader {
	return &demoHeldReader{input: make(chan []byte), release: make(chan []byte), entered: make(chan struct{}), left: make(chan struct{}), cancelled: make(chan struct{}), cancels: cancels}
}

func (r *demoHeldReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	r.reads++
	r.reading = true
	r.mu.Unlock()
	r.enterOnce.Do(func() { close(r.entered) })
	defer func() {
		r.mu.Lock()
		r.reading = false
		r.mu.Unlock()
	}()
	select {
	case data := <-r.input:
		return copy(p, data), nil
	case <-r.release:
		r.leftOnce.Do(func() { close(r.left) })
		return 0, io.EOF
	}
}

func (r *demoHeldReader) Cancel() bool {
	r.mu.Lock()
	r.cancelsN++
	r.mu.Unlock()
	r.cancelOnce.Do(func() { close(r.cancelled) })
	return r.cancels
}

func (r *demoHeldReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	r.closedDuringRead = r.closedDuringRead || r.reading
	return nil
}

func (r *demoHeldReader) unblock() {
	select {
	case <-r.release:
	default:
		close(r.release)
	}
}

func demoAttachResult(t *testing.T, done <-chan struct {
	res client.AttachResult
	err error
}) (client.AttachResult, error) {
	t.Helper()
	select {
	case got := <-done:
		return got.res, got.err
	case <-time.After(5 * time.Second):
		t.Fatal("demo attach did not finish")
		return client.AttachResult{}, nil
	}
}

func startDemoAttach(t *testing.T, ctx context.Context, tty *os.File) <-chan struct {
	res client.AttachResult
	err error
} {
	t.Helper()
	done := make(chan struct {
		res client.AttachResult
		err error
	}, 1)
	go func() {
		defer close(done)
		res, err := newDemoBackend(false).Attach(ctx, client.AttachOptions{In: tty, Out: tty})
		done <- struct {
			res client.AttachResult
			err error
		}{res, err}
	}()
	return done
}

func demoDetachReadBoundary(t *testing.T) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		in, err := os.CreateTemp(t.TempDir(), "tty")
		if err != nil {
			t.Fatal(err)
		}
		reader := newDemoHeldReader(false)
		old := openDemoAttachReader
		openDemoAttachReader = func(io.Reader) (cancelreader.CancelReader, error) { return reader, nil }
		var input *demoInput
		finished := false
		t.Cleanup(func() {
			defer func() { openDemoAttachReader = old }()
			reader.unblock()
			if input != nil {
				if !finished {
					_ = input.finish()
				}
				<-input.readDone
			}
			_ = keyread.Release(in)
			_ = in.Close()
		})
		input, err = startDemoInput(in)
		if err != nil {
			t.Fatal(err)
		}
		input.ask()
		reader.input <- []byte{0x1c}
		if chunk := <-input.keys; len(chunk) != 1 || chunk[0] != 0x1c {
			t.Fatalf("demo detach chunk = %q", chunk)
		}
		// The detach consumer asks for no next chunk. Quiescence exposes an
		// eager Read without relying on which runnable goroutine wins.
		synctest.Wait()
		reader.mu.Lock()
		reads, reading := reader.reads, reader.reading
		reader.mu.Unlock()
		if reads != 1 || reading {
			t.Fatalf("demo read past the completed detach chunk: reads=%d reading=%v", reads, reading)
		}
		finished = true
		if input.finish() {
			t.Fatal("demo detach left an input read running")
		}
	})
}

func TestDemoDetachDoesNotStartAnotherRead(t *testing.T) {
	demoDetachReadBoundary(t)
	terminal := hosttest.NewTerminal(t, 90, 30)
	reader := newDemoHeldReader(false)
	old := openDemoAttachReader
	openDemoAttachReader = func(io.Reader) (cancelreader.CancelReader, error) { return reader, nil }
	var done <-chan struct {
		res client.AttachResult
		err error
	}
	t.Cleanup(func() {
		defer func() { openDemoAttachReader = old }()
		reader.unblock()
		if done != nil {
			for range done {
			}
		}
		_ = keyread.Release(terminal.TTY)
	})
	before, err := term.GetState(int(terminal.TTY.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	done = startDemoAttach(t, context.Background(), terminal.TTY)
	<-reader.entered
	reader.input <- []byte{0x1c}
	res, err := demoAttachResult(t, done)
	if err != nil || !res.Detached || res.ReaderStuck {
		t.Fatalf("detach = %+v, %v, want clean detach", res, err)
	}
	reader.mu.Lock()
	reads, cancelled, closed, under := reader.reads, reader.cancelsN, reader.closes, reader.closedDuringRead
	reader.mu.Unlock()
	if reads != 1 || cancelled != 0 || closed != 1 || under {
		t.Fatalf("reader reads/cancels/closes/closed-under-read = %d/%d/%d/%v", reads, cancelled, closed, under)
	}
	after, err := term.GetState(int(terminal.TTY.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("demo left terminal modes changed: %v", err)
	}
}

func TestDemoTracksStuckReaderAndRestoresTerminal(t *testing.T) {
	for _, cancels := range []bool{false, true} {
		name := "uncancellable"
		cause := keyread.ErrCannotCancel
		if cancels {
			name, cause = "cancel-claims-success", keyread.ErrReadGoesOn
		}
		t.Run(name, func(t *testing.T) {
			terminal := hosttest.NewTerminal(t, 90, 30)
			reader := newDemoHeldReader(cancels)
			oldReader, oldAfter := openDemoAttachReader, demoReleaseAfter
			opened := 0
			openDemoAttachReader = func(io.Reader) (cancelreader.CancelReader, error) { opened++; return reader, nil }
			timeout := make(chan time.Time)
			demoReleaseAfter = func(time.Duration) <-chan time.Time { return timeout }
			t.Cleanup(func() {
				reader.unblock()
				_ = keyread.Release(terminal.TTY)
				openDemoAttachReader, demoReleaseAfter = oldReader, oldAfter
			})
			before, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := startDemoAttach(t, ctx, terminal.TTY)
			<-reader.entered
			cancel()
			<-reader.cancelled
			if cancels {
				close(timeout)
			}
			res, err := demoAttachResult(t, done)
			if err != nil || !res.ReaderStuck {
				t.Fatalf("stuck attach = %+v, %v", res, err)
			}
			if err := keyread.CheckStuck(terminal.TTY); !errors.Is(err, cause) {
				t.Fatalf("stuck cause = %v, want %v", err, cause)
			}
			after, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("stuck demo left terminal modes changed: %v", err)
			}
			_, err = newDemoBackend(false).Attach(ctx, client.AttachOptions{In: terminal.TTY, Out: terminal.TTY})
			if !errors.Is(err, keyread.ErrStuck) || opened != 1 {
				t.Fatalf("next demo = %v, opened=%d, want refusal before another reader", err, opened)
			}
			reader.mu.Lock()
			closed, under := reader.closes, reader.closedDuringRead
			reader.mu.Unlock()
			if closed != 0 || under {
				t.Fatal("closed a demo reader while its read was blocked")
			}
			reader.unblock()
			<-reader.left
			hosttest.WaitFor(t, 5*time.Second, "demo stuck reader cleanup", func() bool { return keyread.CheckStuck(terminal.TTY) == nil })
			reader.mu.Lock()
			closed, under = reader.closes, reader.closedDuringRead
			reader.mu.Unlock()
			if closed != 1 || under {
				t.Fatalf("cleanup closes=%d closed-under-read=%v", closed, under)
			}
		})
	}
}
