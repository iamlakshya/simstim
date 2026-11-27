//go:build !windows

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

type scriptedLauncherReader struct {
	cancels      bool
	ignoreCancel bool
	sendEnter    chan struct{}
	secondRead   chan struct{}
	release      chan struct{}

	mu         sync.Mutex
	reads      int
	reading    bool
	closed     bool
	under      bool
	secondOnce sync.Once
}

func newScriptedLauncherReader(cancels, ignoreCancel bool) *scriptedLauncherReader {
	return &scriptedLauncherReader{
		cancels:      cancels,
		ignoreCancel: ignoreCancel,
		sendEnter:    make(chan struct{}),
		secondRead:   make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (s *scriptedLauncherReader) Read(p []byte) (int, error) {
	s.mu.Lock()
	s.reads++
	n := s.reads
	s.reading = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.reading = false
		s.mu.Unlock()
	}()

	if n == 1 {
		select {
		case <-s.sendEnter:
			p[0] = '\r'
			return 1, nil
		case <-s.release:
			return 0, io.EOF
		}
	}
	s.secondOnce.Do(func() { close(s.secondRead) })
	<-s.release
	return 0, io.EOF
}

func (s *scriptedLauncherReader) Cancel() bool {
	if s.cancels {
		if !s.ignoreCancel {
			s.letGo()
		}
		return true
	}
	return false
}

func (s *scriptedLauncherReader) Close() error {
	s.mu.Lock()
	s.closed = true
	s.under = s.reading
	s.mu.Unlock()
	return nil
}

func (s *scriptedLauncherReader) letGo() {
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

// Using the real tea.NewProgram on a PTY, when the launcher's CancelReader
// cannot stop its blocked Read on attach handoff (either Cancel() == false or
// Cancel() == true that times out), runWith refuses to call be.Attach, leaves
// the reader unclosed until Read exits, and registers HoldStuck so a later
// reader cannot compete on the same terminal.
func TestLauncherRefusesAttachHandoffWhenReaderStuckInRealProgram(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	cases := []struct {
		name         string
		cancels      bool
		ignoreCancel bool
		wantCause    error
	}{
		{name: "uncancellable", cancels: false, ignoreCancel: false, wantCause: keyread.ErrCannotCancel},
		{name: "cancel-timeout", cancels: true, ignoreCancel: true, wantCause: keyread.ErrReadGoesOn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := hosttest.NewTerminal(t, 100, 30)
			oldErr := os.Stderr
			os.Stderr = term.TTY
			restoreFiles := SetLauncherFiles(term.TTY, term.TTY)
			restoreWait := SetLauncherReleaseWait(15 * time.Millisecond)

			sr := newScriptedLauncherReader(tc.cancels, tc.ignoreCancel)
			restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
				return sr, nil
			})
			t.Cleanup(func() {
				sr.letGo()
				_ = keyread.Release(term.TTY)
				restoreReader()
				restoreWait()
				restoreFiles()
				os.Stderr = oldErr
			})

			be := newFakeBackend()
			be.hosts = []client.Host{{Name: "box", SSH: "box"}}
			s := client.Session{ID: "abcd2345", Name: "auth-fix", Agent: "claude", State: "idle", Alive: true}
			be.state["box"] = &fakeHost{sessions: []client.Session{s}}
			be.rememberLastHook = func(_, _, _ string) {
				<-sr.secondRead
			}

			done := make(chan error, 1)
			go func() {
				done <- runWith(context.Background(), be, Options{Now: func() time.Time { return now }})
			}()

			term.WaitFor("auth-fix")
			close(sr.sendEnter)

			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("runWith did not finish")
			}

			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Code != "terminal_busy" {
				t.Fatalf("runWith err = %v (ue=%+v), want terminal_busy", err, ue)
			}
			if !errors.Is(err, client.ErrAttachReaderStuck) || !errors.Is(err, keyread.ErrStuck) {
				t.Fatalf("runWith err = %v, want ErrAttachReaderStuck and keyread.ErrStuck", err)
			}
			if !strings.Contains(ue.Hint, "attach box/abcd2345") {
				t.Errorf("hint = %q, want attach box/abcd2345", ue.Hint)
			}
			if len(be.attached) != 0 {
				t.Fatalf("be.Attach was called %d times while launcher reader remained blocked", len(be.attached))
			}

			sr.mu.Lock()
			closedWhileBlocked := sr.closed
			sr.mu.Unlock()
			if closedWhileBlocked {
				t.Fatal("launcher closed CancelReader while Read remained blocked")
			}
			if serr := keyread.CheckStuck(term.TTY); !errors.Is(serr, keyread.ErrStuck) || !errors.Is(serr, tc.wantCause) {
				t.Fatalf("CheckStuck while blocked = %v, want ErrStuck + %v", serr, tc.wantCause)
			}

			// A second launcher start on the stuck terminal fails before opening a reader.
			if err2 := runWith(context.Background(), be, Options{Now: func() time.Time { return now }}); !errors.Is(err2, keyread.ErrStuck) {
				t.Fatalf("second runWith on stuck terminal = %v, want keyread.ErrStuck", err2)
			}

			// Releasing the stuck Read allows CheckStuck to close sr cleanly.
			sr.letGo()
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
				if keyread.CheckStuck(term.TTY) == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("stuck launcher reader never cleared after Read returned")
				}
			}
			sr.mu.Lock()
			closedAfter, under := sr.closed, sr.under
			sr.mu.Unlock()
			if !closedAfter || under {
				t.Fatalf("after unblock: closed=%v under=%v", closedAfter, under)
			}
		})
	}
}
