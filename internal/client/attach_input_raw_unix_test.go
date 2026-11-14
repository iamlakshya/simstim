//go:build darwin || linux

package client

import (
	"errors"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/keyread"
)

// heldAttachTTYReader borrows the terminal. Its physical read can only end
// when the test writes a line. Closing the reader never closes that file.
type heldAttachTTYReader struct {
	in      *os.File
	entered chan struct{}
	reading atomic.Bool
	closed  atomic.Bool
	under   atomic.Bool
}

func (r *heldAttachTTYReader) Read(p []byte) (int, error) {
	r.reading.Store(true)
	close(r.entered)
	defer r.reading.Store(false)
	return r.in.Read(p)
}

func (*heldAttachTTYReader) Cancel() bool { return false }

func (r *heldAttachTTYReader) Close() error {
	if r.reading.Load() {
		r.under.Store(true)
	}
	r.closed.Store(true)
	return nil
}

// A previously cached reader must not change a real terminal's modes while
// Attach still owns a read of it. Once that read ends, Raw works again and
// its restore puts the terminal back in its first state.
func TestCachedReaderRawRefusesAStuckAttachOnTheSameTerminal(t *testing.T) {
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	fd := int(terminal.Fd())
	original, err := term.GetState(fd)
	if err != nil {
		_ = terminal.Close()
		_ = master.Close()
		t.Fatal(err)
	}
	var input *attachInput
	var finish func() bool
	var restoreRaw func() error
	restoreReader := func() {}
	release := sync.OnceFunc(func() {
		_ = term.Restore(fd, original)
		if _, err := master.Write([]byte("release\n")); err != nil {
			t.Errorf("release the held terminal read: %v", err)
		}
	})
	t.Cleanup(func() {
		defer restoreReader()
		if restoreRaw != nil {
			_ = restoreRaw()
		}
		if input != nil {
			finish()
			release()
			select {
			case <-input.readDone:
			case <-time.After(5 * time.Second):
				// Neither side may close under the physical read. Keep them
				// open until this failed test process exits.
				t.Error("the held terminal read did not end before cleanup")
				return
			}
		}
		_ = term.Restore(fd, original)
		if err := keyread.Release(terminal); err != nil {
			t.Errorf("the cached reader still owns the terminal: %v", err)
			return
		}
		_ = terminal.Close()
		_ = master.Close()
	})

	cached, err := keyread.For(terminal)
	if err != nil {
		t.Fatal(err)
	}
	r := &heldAttachTTYReader{in: terminal, entered: make(chan struct{})}
	restoreReader = SetAttachReader(func(_ io.Reader) (cancelreader.CancelReader, error) {
		return r, nil
	})
	input, err = startAttachInput(terminal, make(chan []byte, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	finish = sync.OnceValue(input.finish)
	input.ask()
	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("attach never entered its physical terminal read")
	}
	if !finish() {
		t.Fatal("the held physical read did not keep its terminal ownership")
	}
	before, err := term.GetState(fd)
	if err != nil {
		t.Fatal(err)
	}
	restoreRaw, err = cached.Raw()
	if restoreRaw != nil || !errors.Is(err, keyread.ErrStuck) || !errors.Is(err, keyread.ErrCannotCancel) {
		t.Errorf("cached Raw while attach read is held = %v, restore %v, want ErrStuck and ErrCannotCancel", err, restoreRaw != nil)
	}
	after, stateErr := term.GetState(fd)
	if stateErr != nil || !reflect.DeepEqual(before, after) {
		t.Errorf("cached Raw changed the terminal while attach read was held: before=%+v after=%+v err=%v", before, after, stateErr)
	}
	if restoreRaw != nil {
		_ = restoreRaw()
		restoreRaw = nil
	}
	if r.closed.Load() || r.under.Load() {
		t.Error("the held attach reader closed before its physical read ended")
	}

	release()
	select {
	case <-input.readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the physical terminal read did not end after release")
	}
	if err := keyread.CheckStuck(terminal); err != nil {
		t.Fatal(err)
	}
	if !r.closed.Load() || r.under.Load() {
		t.Error("the released attach reader did not close safely")
	}
	restoreRaw, err = cached.Raw()
	if err != nil || restoreRaw == nil {
		t.Fatalf("cached Raw after attach read ended = %v, want a usable terminal", err)
	}
	if err := restoreRaw(); err != nil {
		t.Fatal(err)
	}
	restoreRaw = nil
	if after, err := term.GetState(fd); err != nil || !reflect.DeepEqual(original, after) {
		t.Errorf("Raw recovery did not restore the original mode: before=%+v after=%+v err=%v", original, after, err)
	}
}
