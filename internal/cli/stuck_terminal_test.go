//go:build !windows

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type stuckTestReader struct {
	entered chan struct{}
	release chan struct{}

	mu      sync.Mutex
	reading bool
	closed  bool
	under   bool
	once    sync.Once
}

func newStuckTestReader() *stuckTestReader {
	return &stuckTestReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *stuckTestReader) Read([]byte) (int, error) {
	s.mu.Lock()
	s.reading = true
	s.mu.Unlock()
	s.once.Do(func() { close(s.entered) })
	<-s.release
	s.mu.Lock()
	s.reading = false
	s.mu.Unlock()
	return 0, io.EOF
}

func (s *stuckTestReader) Cancel() bool { return false }

func (s *stuckTestReader) Close() error {
	s.mu.Lock()
	s.closed = true
	s.under = s.reading
	s.mu.Unlock()
	return nil
}

func (s *stuckTestReader) letGo() {
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// When a guided setup installer session ends while client.Attach's terminal
// reader remains stuck on os.Stdin, guide records the completed install step
// and stops with terminal_busy before opening the subsequent "Log in now?"
// question, and keyread.For refuses to hand out the cached terminal reader
// until the stuck Read exits.
func TestGuidedSetupStopsBeforeQuestionWhenInstallerReaderStuck(t *testing.T) {
	th := newTestHost(t)
	term := hosttest.NewTerminal(t, 90, 30)

	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = term.TTY, term.TTY, term.TTY

	sr := newStuckTestReader()
	restoreAttachReader := client.SetAttachReader(func(io.Reader) (cancelreader.CancelReader, error) {
		return sr, nil
	})
	t.Cleanup(func() {
		sr.letGo()
		_ = keyread.Release(term.TTY)
		restoreAttachReader()
		os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	})

	var out syncBuffer
	g := newGuide(context.Background(), guideOpts{}, &guideLocal{})
	g.in, g.out, g.tty = term.TTY, &out, true
	g.p = &prompter{ctx: g.ctx, in: term.TTY, out: &out, on: true, path: brand.Name + " setup"}
	g.checks = func(client.Host) ([]api.AgentCheck, error) {
		return []api.AgentCheck{
			readyCheck("claude", "Claude Code", api.LoginNo),
			readyCheck("codex", "Codex", api.LoginNo),
		}, nil
	}

	f := onboard.NewFlow(th.ch.Name, []api.AgentCheck{
		readyCheck("claude", "Claude Code", api.LoginNo),
	}, g.log)
	for i := range f.Items {
		if f.Items[i].Agent.Kind == "codex" {
			f.Items[i].Agent.Setup.Install = "echo INSTALLED_LOCAL_CODEX"
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- g.agents(th.ch, f)
	}()

	// Answer the checklist on the real PTY ("2\r" ticks Codex), which caches
	// keyread.For(term.TTY) before Attach runs.
	hosttest.WaitFor(t, 5*time.Second, "checklist prompt", func() bool {
		return strings.Contains(out.String(), "Type the numbers to install")
	})
	term.Type("2\r")

	<-sr.entered

	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("g.agents did not finish; output so far:\n%s", out.String())
	}

	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "terminal_busy" {
		t.Fatalf("g.agents err = %v (ue=%+v), want terminal_busy", err, ue)
	}
	wantHint := brand.Name + " setup --host box (continues setup in a new command)"
	if ue.Hint != wantHint {
		t.Errorf("hint = %q, want %q", ue.Hint, wantHint)
	}
	if strings.Contains(out.String(), "Log in now?") {
		t.Fatalf("guide opened a follow-up question while reader was stuck:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "! Codex") {
		t.Errorf("guide did not record completed install line before stopping:\n%s", out.String())
	}

	// Even though keyread.For(term.TTY) was cached by the checklist prompt,
	// For must refuse while Attach's reader remains stuck on term.TTY.
	if _, kerr := keyread.For(term.TTY); !errors.Is(kerr, keyread.ErrStuck) {
		t.Fatalf("keyread.For(term.TTY) while stuck = %v, want ErrStuck", kerr)
	}

	// Once Attach's reader exits, keyread.For(term.TTY) works again.
	sr.letGo()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if keyread.CheckStuck(term.TTY) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stuck attach reader never cleared after Read returned")
		}
	}
	if _, kerr := keyread.For(term.TTY); kerr != nil {
		t.Fatalf("keyread.For(term.TTY) after unblock = %v, want nil", kerr)
	}
}

// closedTerminalReader records any forbidden read after a terminal's factory
// has made its file unavailable. It never blocks a failing regression test.
type closedTerminalReader struct {
	reads  atomic.Int32
	closes atomic.Int32
}

func (r *closedTerminalReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}

func (*closedTerminalReader) Cancel() bool { return true }

func (r *closedTerminalReader) Close() error {
	r.closes.Add(1)
	return nil
}

// On a terminal, neither a question (readLineOf / readTerminalLine) nor inputOf
// ever falls back to cooked plainInput line reading when keyread fails or when
// an earlier reader is stuck: pasted "yes\n" waiting on the PTY never becomes
// an affirmative answer.
func TestTerminalPromptNeverFallsBackToPlainLineOnReaderFailure(t *testing.T) {
	t.Run("keyread-open-fails", func(t *testing.T) {
		term := hosttest.NewTerminal(t, 90, 30)
		oldKeyOpen := keyread.Open
		oldNewKey := newKeyReader
		keyread.Open = func(io.Reader) (cancelreader.CancelReader, error) {
			return nil, errors.New("raw reader unavailable")
		}
		newKeyReader = func(*os.File) (cancelreader.CancelReader, error) {
			return nil, errors.New("cancel reader unavailable")
		}
		t.Cleanup(func() {
			_ = keyread.Release(term.TTY)
			keyread.Open = oldKeyOpen
			newKeyReader = oldNewKey
		})

		term.Type("yes\n")

		var out bytes.Buffer
		p := &prompter{
			ctx:     context.Background(),
			in:      term.TTY,
			out:     &out,
			on:      true,
			line:    []string{"rm", "box/ss"},
			flagsAt: 2,
		}
		ok, err := p.yesNo("Delete auth-fix on box?", false)
		var ue *client.UserError
		if ok || !errors.As(err, &ue) || ue.Code != "terminal_failed" || ue.Hint == "" {
			t.Fatalf("yesNo on broken terminal = (%v, %v), want false + terminal_failed", ok, err)
		}
		if out.Len() != 0 {
			t.Errorf("prompt printed %q before terminal reader opened", out.String())
		}

		r, closeFn, ierr := inputOf(term.TTY)
		ue = nil
		if r != nil || closeFn != nil || !errors.As(ierr, &ue) || ue.Code != "terminal_failed" {
			t.Fatalf("inputOf on broken terminal = (%v, %v), want nil + terminal_failed", r, ierr)
		}
	})

	t.Run("raw-mode-fails", func(t *testing.T) {
		term := hosttest.NewTerminal(t, 90, 30)
		if !isTerminal(term.TTY) {
			t.Fatal("the raw-mode failure fixture did not start on a terminal")
		}
		oldKeyOpen := keyread.Open
		r := &closedTerminalReader{}
		factoryClosed := false
		keyread.Open = func(in io.Reader) (cancelreader.CancelReader, error) {
			if in != term.TTY {
				return nil, errors.New("the reader factory received another input")
			}
			// For has saved the state before opening its reader. Closing
			// this fresh owned file now forces the later Raw operation to
			// fail. No physical Read has started on it.
			if err := term.TTY.Close(); err != nil {
				return nil, err
			}
			factoryClosed = true
			return r, nil
		}
		t.Cleanup(func() {
			_ = keyread.Release(term.TTY)
			keyread.Open = oldKeyOpen
			if r.closes.Load() != 1 {
				t.Errorf("the failed raw-mode reader closed %d times, want once", r.closes.Load())
			}
		})
		term.Type("yes\n")
		var out bytes.Buffer
		p := testPrompts("", &out)
		p.in = term.TTY
		ok, err := p.yesNo("Delete auth-fix on box?", false)
		var ue *client.UserError
		if ok || !errors.As(err, &ue) || ue.Code != "terminal_failed" || ue.Hint == "" {
			t.Errorf("yesNo after raw-mode failure = (%v, %v), want false + terminal_failed with a hint", ok, err)
		} else if !strings.Contains(ue.Msg, "raw mode") {
			t.Errorf("the raw-mode failure lost its operation: %s", ue.Msg)
		}
		if !factoryClosed {
			t.Error("the raw-mode failure fixture did not reach its reader factory")
		}
		if r.reads.Load() != 0 {
			t.Errorf("the question read %d times after raw mode failed, want none", r.reads.Load())
		}
		if out.Len() != 0 {
			t.Errorf("raw-mode failure printed a plain-line question: %q", out.String())
		}
	})

	t.Run("terminal-busy", func(t *testing.T) {
		term := hosttest.NewTerminal(t, 90, 30)
		// Pre-cache keyread.For(term.TTY) first, then hold a stuck reader.
		if _, err := keyread.For(term.TTY); err != nil {
			t.Fatal(err)
		}
		stuckDone := make(chan struct{})
		closedHeld := false
		keyread.HoldStuck(term.TTY, func() error {
			closedHeld = true
			return nil
		}, stuckDone, keyread.ErrCannotCancel)
		t.Cleanup(func() {
			select {
			case <-stuckDone:
			default:
				close(stuckDone)
			}
			_ = keyread.Release(term.TTY)
		})

		term.Type("yes\n")

		var out bytes.Buffer
		p := &prompter{
			ctx:     context.Background(),
			in:      term.TTY,
			out:     &out,
			on:      true,
			line:    []string{"rm", "box/ss"},
			flagsAt: 2,
		}
		ok, err := p.yesNo("Delete auth-fix on box?", false)
		var ue *client.UserError
		if ok || !errors.As(err, &ue) || ue.Code != "terminal_busy" || ue.Hint == "" {
			t.Fatalf("yesNo on busy terminal = (%v, %v), want false + terminal_busy", ok, err)
		}

		r, closeFn, ierr := inputOf(term.TTY)
		ue = nil
		if r != nil || closeFn != nil || !errors.As(ierr, &ue) || ue.Code != "terminal_busy" {
			t.Fatalf("inputOf on busy terminal = (%v, %v), want nil + terminal_busy", r, ierr)
		}

		close(stuckDone)
		if err := keyread.CheckStuck(term.TTY); err != nil || !closedHeld {
			t.Fatalf("after stuckDone closed: err=%v closedHeld=%v", err, closedHeld)
		}
	})
}
