package hosttest

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/keyread"
)

// stuck is a terminal's reader whose read waits until the test lets it
// go, and whose Cancel fails, as on some Windows consoles.
type stuck struct {
	entered, release chan struct{}
}

func (s *stuck) Read([]byte) (int, error) {
	s.entered <- struct{}{}
	<-s.release
	return 0, io.EOF
}

func (*stuck) Cancel() bool { return false }
func (*stuck) Close() error { return nil }

// closeBoth closes both sides of a terminal that its cleanup left open, and
// waits until the reader of its other side stops: a file that a read waits
// on is closed only when that read returns.
func closeBoth(t *testing.T, term *Terminal) {
	t.Helper()
	_ = term.TTY.Close()
	_ = term.pty.Close()
	select {
	case <-term.done:
	case <-time.After(5 * time.Second):
		t.Error("the reader of the terminal's other side never stopped")
	}
}

// openFiles counts the files that this process has open.
func openFiles(t *testing.T) int {
	t.Helper()
	dir, err := os.Open("/dev/fd")
	if err != nil {
		t.Skipf("no /dev/fd to count open files: %v", err)
	}
	// Names need no metadata lookup on descriptors that can close while the
	// directory is read. The directory's own file is counted on both calls.
	fds, err := dir.Readdirnames(-1)
	closeErr := dir.Close()
	if err != nil {
		t.Fatalf("count open files in /dev/fd: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close /dev/fd after counting open files: %v", closeErr)
	}
	return len(fds)
}

// A terminal whose read cannot be stopped stays open at the end of its
// test: its cleanup never closes it under the read. Without such a read,
// the cleanup closes it. The test then lets the read go, and closes both
// sides of the terminal itself: no terminal outlives a run, and the
// process has as many files open after it as before.
func TestATerminalStaysOpenUnderARead(t *testing.T) {
	// A terminal first, so that what the first one opens once for good,
	// such as the poller, is open before the count.
	t.Run("warm up", func(t *testing.T) { NewTerminal(t, 80, 10) })
	files := openFiles(t)
	src := &stuck{entered: make(chan struct{}, 1), release: make(chan struct{})}
	old := keyread.Open
	defer func() { keyread.Open = old }()
	keyread.Open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
	var held, free *Terminal
	t.Run("a read waits", func(t *testing.T) {
		held = NewTerminal(t, 80, 10)
		r, err := keyread.For(held.TTY)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-src.entered
			cancel()
		}()
		_, _ = r.ReadKey(ctx) // the read now waits, and cannot be stopped
	})
	if _, err := held.TTY.Stat(); err != nil {
		t.Errorf("the cleanup closed the terminal under a read that it could not stop: %v", err)
	}
	close(src.release) // the read ends, and touches no terminal any more
	closeBoth(t, held)
	if _, err := held.pty.Stat(); err == nil {
		t.Error("the terminal's other side is still open")
	}
	keyread.Open = old
	t.Run("no read waits", func(t *testing.T) {
		free = NewTerminal(t, 80, 10)
	})
	if _, err := free.TTY.Stat(); err == nil {
		t.Error("the cleanup left a terminal open with no read of it")
	}
	if n := openFiles(t); n != files {
		t.Errorf("%d files open after the test, %d before", n, files)
	}
}

// goesOn is a terminal's reader whose read takes its cancel, and still
// waits until the test lets it go: a bug in a reader.
type goesOn struct {
	entered, release chan struct{}
}

func (g *goesOn) Read([]byte) (int, error) {
	g.entered <- struct{}{}
	<-g.release
	return 0, io.EOF
}

func (*goesOn) Cancel() bool { return true }
func (*goesOn) Close() error { return nil }

// observed is a test whose failures the test itself looks at: it keeps the
// cleanups and the errors, and passes anything else on.
type observed struct {
	testing.TB
	cleanups []func()
	errors   []string
}

func (o *observed) Cleanup(f func()) { o.cleanups = append(o.cleanups, f) }
func (o *observed) Errorf(format string, args ...any) {
	o.errors = append(o.errors, fmt.Sprintf(format, args...))
}
func (o *observed) Error(args ...any) { o.errors = append(o.errors, fmt.Sprint(args...)) }

func (o *observed) cleanUp() {
	for i := len(o.cleanups) - 1; i >= 0; i-- {
		o.cleanups[i]()
	}
}

// A read that took its cancel and still goes on past ReleaseWait is a bug:
// the terminal's cleanup fails the test, and leaves the terminal open.
func TestACancelledReadThatGoesOnFailsTheTest(t *testing.T) {
	src := &goesOn{entered: make(chan struct{}, 1), release: make(chan struct{})}
	old, wait := keyread.Open, keyread.ReleaseWait
	keyread.Open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
	keyread.ReleaseWait = 10 * time.Millisecond
	defer func() { keyread.Open, keyread.ReleaseWait = old, wait }()
	o := &observed{TB: t}
	term := NewTerminal(o, 80, 10)
	r, err := keyread.For(term.TTY)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-src.entered
		cancel()
	}()
	_, _ = r.ReadKey(ctx) // the read now waits, and its cancel will not end it
	o.cleanUp()
	if len(o.errors) != 1 || !strings.Contains(o.errors[0], keyread.ErrReadGoesOn.Error()) {
		t.Errorf("the cleanup reported %q", o.errors)
	}
	if _, err := term.TTY.Stat(); err != nil {
		t.Errorf("the cleanup closed the terminal under the read: %v", err)
	}
	close(src.release)
	closeBoth(t, term)
}
