//go:build darwin || linux

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	xterm "golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/keyread"
)

// A signal, the end of the context and esc each close the picker as a
// cancel, and each leaves no row of it on the screen.
func TestPickErasesItsRowsAtEveryEnd(t *testing.T) {
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGHUP)
	defer signal.Stop(caught)
	for _, end := range []string{"a signal", "the end of the context", "esc"} {
		term := newPickTerminal(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := runPickIn(ctx, term, PickOptions{Title: "attach", Rows: pickSample()})
		term.WaitFor("needs-you")
		switch end {
		case "a signal":
			if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
				t.Fatal(err)
			}
			select {
			case <-caught:
			case <-time.After(5 * time.Second):
				t.Fatal("the signal never came")
			}
		case "the end of the context":
			cancel()
		case "esc":
			term.Type("\x1b")
		}
		res := waitPick(t, term, done)
		cancel()
		if res.err != ErrPickCancelled { //nolint:errorlint // Pick returns it as it is
			t.Errorf("%s: picked %s, %v", end, res.r.Address(), res.err)
		}
		if ok, screen := erased(term, 70, 20); !ok {
			t.Errorf("%s left the picker's rows:\n%s", end, screen)
		}
	}
}

// failing is a terminal's reader whose read waits until the test makes it
// fail.
type failing struct {
	entered chan struct{}
	fail    chan struct{}
}

func (f *failing) Read([]byte) (int, error) {
	f.entered <- struct{}{}
	<-f.fail
	return 0, errors.New("the terminal went away")
}

func (f *failing) Cancel() bool { return true }
func (*failing) Close() error   { return nil }

// A terminal that cannot be read closes the picker as a cancel, and the
// picker erases its rows.
func TestPickEndsWhenTheTerminalFails(t *testing.T) {
	src := &failing{entered: make(chan struct{}, 1), fail: make(chan struct{})}
	old := keyread.Open
	keyread.Open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
	defer func() { keyread.Open = old }()
	term := newPickTerminal(t)
	done := runPick(term, PickOptions{Title: "attach", Rows: pickSample()})
	term.WaitFor("needs-you")
	<-src.entered
	close(src.fail)
	if res := waitPick(t, term, done); res.err != ErrPickCancelled { //nolint:errorlint // Pick returns it as it is
		t.Errorf("a read error: picked %s, %v", res.r.Address(), res.err)
	}
	if ok, screen := erased(term, 70, 20); !ok {
		t.Errorf("a read error left the picker's rows:\n%s", screen)
	}
}

// The end of the context after a pick, as the picker ends or after the
// terminal is restored, is a cancel: never a pick.
func TestPickCancelAfterAPick(t *testing.T) {
	defer func() { pickStage = func(string) {} }()
	for _, stage := range []string{"picked", "restored"} {
		ctx, cancel := context.WithCancel(context.Background())
		pickStage = func(at string) {
			if at == stage {
				cancel()
			}
		}
		term := newPickTerminal(t)
		done := runPickIn(ctx, term, PickOptions{Title: "attach", Rows: pickSample()})
		term.WaitFor("needs-you")
		term.Type("\r")
		res := waitPick(t, term, done)
		cancel()
		if res.err != ErrPickCancelled { //nolint:errorlint // Pick returns it as it is
			t.Errorf("the context ended at %s: picked %s, %v", stage, res.r.Address(), res.err)
		}
	}
}

// A SIGHUP or a SIGQUIT at any stage of the picker, from before its raw
// mode to after the terminal is restored, closes it as cancelled, and
// leaves the terminal as it was. The test catches the signals too, so a
// signal that the picker misses does not end the test.
func TestPickSignalAtEveryStage(t *testing.T) {
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(caught)
	defer func() { pickStage = func(string) {} }()
	for _, stage := range []string{"start", "raw", "picked", "restored"} {
		for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGQUIT} {
			pickStage = func(at string) {
				if at != stage {
					return
				}
				if err := syscall.Kill(os.Getpid(), sig); err != nil {
					t.Error(err)
				}
				// Once the test has the signal, every handler has it.
				select {
				case <-caught:
				case <-time.After(5 * time.Second):
					t.Error("the signal never came")
				}
			}
			term := newPickTerminal(t)
			before, err := xterm.GetState(int(term.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			done := runPick(term, PickOptions{Title: "attach", Rows: pickSample()})
			if stage == "picked" || stage == "restored" {
				term.WaitFor("needs-you")
				term.Type("\r")
			}
			res := waitPick(t, term, done)
			if res.err != ErrPickCancelled { //nolint:errorlint // Pick returns it as it is
				t.Errorf("%v at %s: picked %s, %v", sig, stage, res.r.Address(), res.err)
			}
			after, err := xterm.GetState(int(term.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("%v at %s left the terminal changed", sig, stage)
			}
		}
	}
}
