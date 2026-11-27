//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

func probeTerminalFiles(t *testing.T) (*hosttest.Terminal, *hosttest.Terminal, *term.State) {
	t.Helper()
	hosttest.New(t)
	in, out := hosttest.NewTerminal(t, 90, 30), hosttest.NewTerminal(t, 90, 30)
	original, err := term.GetState(int(in.TTY.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut, oldFactory := os.Stdin, os.Stdout, newKeyReader
	os.Stdin, os.Stdout = in.TTY, out.TTY
	t.Cleanup(func() {
		_ = keyread.Release(in.TTY)
		_ = term.Restore(int(in.TTY.Fd()), original)
		os.Stdin, os.Stdout, newKeyReader = oldIn, oldOut, oldFactory
	})
	return in, out, original
}

// A marker written after the command makes the PTY collector's earlier
// bytes observable before checking that an ownership failure sent no query.
func finishedProbeOutput(t *testing.T, out *hosttest.Terminal) []byte {
	t.Helper()
	const marker = "[terminal-probe-test-finished]"
	if _, err := out.TTY.WriteString(marker); err != nil {
		t.Fatal(err)
	}
	got := out.WaitFor(marker)
	before, _, _ := bytes.Cut(got, []byte(marker))
	return before
}

func TestTerminalProbeRefusesReaderFailuresBeforeQueries(t *testing.T) {
	for _, mode := range []string{"busy", "factory-error", "prepared-factory-error"} {
		t.Run(mode, func(t *testing.T) {
			in, out, original := probeTerminalFiles(t)
			fd := int(in.TTY.Fd())
			var opens atomic.Int32
			failure := errors.New("the probe reader could not open")
			newKeyReader = func(*os.File) (cancelreader.CancelReader, error) {
				opens.Add(1)
				if mode == "prepared-factory-error" {
					if _, err := term.MakeRaw(fd); err != nil {
						return nil, err
					}
				}
				return nil, failure
			}
			want := "terminal_failed"
			if mode == "busy" {
				want = "terminal_busy"
				done := make(chan struct{})
				keyread.HoldStuck(in.TTY, func() error { return nil }, done, keyread.ErrCannotCancel)
				t.Cleanup(func() { close(done); _ = keyread.Release(in.TTY) })
			}
			err := terminalProbe(context.Background())
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Code != want || ue.Hint == "" {
				t.Errorf("reader failure: err=%v ue=%+v, want %s", err, ue, want)
			}
			if mode == "busy" && opens.Load() != 0 || mode != "busy" && opens.Load() != 1 {
				t.Errorf("factory opens=%d for %s", opens.Load(), mode)
			}
			if mode != "busy" && (err == nil || !strings.Contains(err.Error(), failure.Error())) {
				t.Errorf("factory failure was lost: %v", err)
			}
			after, stateErr := term.GetState(fd)
			if stateErr != nil || !reflect.DeepEqual(original, after) {
				t.Errorf("rejected probe changed terminal modes: %v", stateErr)
			}
			if got := finishedProbeOutput(t, out); len(got) != 0 {
				t.Errorf("rejected probe sent queries or a false report: %q", got)
			}
		})
	}
}

func TestTerminalProbeReportsRawFailureAndClosesItsReader(t *testing.T) {
	_, out, _ := probeTerminalFiles(t)
	plain, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	var reads, closes atomic.Int32
	newKeyReader = func(f *os.File) (cancelreader.CancelReader, error) {
		// The factory returns a reader, but the subsequent raw-mode syscall
		// fails. No physical Read has started, so replacing this owned test
		// descriptor cannot race a terminal read.
		if err := unix.Dup2(int(plain.Fd()), int(f.Fd())); err != nil {
			return nil, err
		}
		return preparedConsoleReader{
			Reader: probeReadCounter{&reads},
			cancel: func() bool { return true },
			close:  func() error { closes.Add(1); return nil },
		}, nil
	}
	err = terminalProbe(context.Background())
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "terminal_failed" || ue.Hint == "" {
		t.Errorf("raw-mode failure: err=%v ue=%+v", err, ue)
	}
	if reads.Load() != 0 || closes.Load() != 1 {
		t.Errorf("failed raw setup: reads=%d closes=%d", reads.Load(), closes.Load())
	}
	if got := finishedProbeOutput(t, out); len(got) != 0 {
		t.Errorf("failed raw setup sent queries or a false report: %q", got)
	}
}

type probeReadCounter struct{ reads *atomic.Int32 }

func (r probeReadCounter) Read([]byte) (int, error) { r.reads.Add(1); return 0, io.EOF }

func TestTerminalProbeRestoresBeforeAndAfterDelayedReaderClose(t *testing.T) {
	in, out, original := probeTerminalFiles(t)
	fd := int(in.TTY.Fd())
	held := newHeldKeyReader(false)
	var openedInOriginal atomic.Bool
	newKeyReader = func(*os.File) (cancelreader.CancelReader, error) {
		before, err := term.MakeRaw(fd)
		if err != nil {
			return nil, err
		}
		openedInOriginal.Store(reflect.DeepEqual(before, original))
		return preparedConsoleReader{
			Reader: held,
			cancel: held.Cancel,
			close: func() error {
				_ = held.Close()
				return term.Restore(fd, before)
			},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ended := make(chan struct{})
	go func() { defer close(ended); done <- terminalProbe(ctx) }()
	t.Cleanup(func() {
		cancel()
		held.letGo()
		<-ended
		hosttest.WaitFor(t, 5*time.Second, "the delayed probe reader to release", func() bool {
			return keyread.CheckStuck(in.TTY) == nil
		})
	})
	select {
	case <-held.started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not begin its terminal read")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("probe cancellation=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("probe did not leave its blocked reader")
	}
	<-ended
	if !openedInOriginal.Load() || held.closes.Load() != 0 {
		t.Errorf("factory-before-raw=%v reader closes=%d", openedInOriginal.Load(), held.closes.Load())
	}
	after, err := term.GetState(fd)
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Errorf("probe did not restore its original state before late close: %v", err)
	}
	if err := keyread.CheckStuck(in.TTY); !errors.Is(err, keyread.ErrStuck) {
		t.Errorf("probe lost ownership of the live reader: %v", err)
	}
	held.letGo()
	hosttest.WaitFor(t, 5*time.Second, "the probe's native close", func() bool {
		return keyread.CheckStuck(in.TTY) == nil
	})
	after, err = term.GetState(fd)
	if err != nil || !reflect.DeepEqual(original, after) || held.closes.Load() != 1 || held.closedDuringRead.Load() {
		t.Errorf("late close clobbered the restored console: err=%v closes=%d under-read=%v", err, held.closes.Load(), held.closedDuringRead.Load())
	}
	_ = finishedProbeOutput(t, out)
}
