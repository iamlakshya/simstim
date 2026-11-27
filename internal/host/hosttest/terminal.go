package hosttest

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/iamlakshya/simstim/internal/keyread"
)

// Terminal is a pseudo-terminal that stands in for the user's terminal: the
// client gets TTY as its input and output, and the test types into it and
// reads what the client draws.
type Terminal struct {
	// TTY is the terminal's side for the program under test.
	TTY *os.File
	pty *os.File
	t   testing.TB

	mu   sync.Mutex
	out  []byte
	done chan struct{}
}

// NewTerminal opens a terminal of cols by rows. It is closed at the end of
// the test.
func NewTerminal(t testing.TB, cols, rows int) *Terminal {
	t.Helper()
	p, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	term := &Terminal{TTY: tty, pty: p, t: t, done: make(chan struct{})}
	term.Resize(cols, rows, false)
	go func() {
		defer close(term.done)
		buf := make([]byte, 32<<10)
		for {
			n, err := p.Read(buf)
			term.mu.Lock()
			term.out = append(term.out, buf[:n]...)
			term.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		// A question or the picker may have left a read of the terminal
		// waiting: stop it before the terminal closes under it. When the
		// read cannot be stopped, the terminal stays open, and its other
		// side and that side's reader with it: the test leaks them rather
		// than close the terminal under the read. A reader that cannot
		// cancel is the terminal's way. A cancelled read that goes on is
		// a bug.
		switch err := keyread.Release(tty); {
		case errors.Is(err, keyread.ErrCannotCancel):
			t.Logf("the terminal stays open: %v", err)
			return
		case err != nil:
			t.Errorf("the terminal stays open: %v", err)
			return
		}
		_ = tty.Close()
		_ = p.Close()
		select {
		case <-term.done:
		case <-time.After(5 * time.Second):
			t.Error("the terminal stayed open after the test: something still reads it")
		}
	})
	return term
}

// Output returns everything drawn so far.
func (term *Terminal) Output() []byte {
	term.mu.Lock()
	defer term.mu.Unlock()
	return bytes.Clone(term.out)
}

// Clear forgets what was drawn so far.
func (term *Terminal) Clear() {
	term.mu.Lock()
	defer term.mu.Unlock()
	term.out = nil
}

// WaitFor waits until the output holds want, and returns the output.
func (term *Terminal) WaitFor(want string) []byte {
	term.t.Helper()
	var out []byte
	deadline := time.Now().Add(15 * time.Second)
	for {
		out = term.Output()
		if bytes.Contains(out, []byte(want)) {
			return out
		}
		if time.Now().After(deadline) {
			term.t.Fatalf("the terminal never showed %q; it shows %q", want, tail(out, 2000))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

// Screen returns what a blank terminal of cols by rows shows after out: the
// text of each row, and whether it is on the alternate screen.
func Screen(out []byte, cols, rows int) (lines []string, alt bool) {
	e := vt.NewEmulator(cols, rows)
	// The emulator writes replies to queries into a pipe that nothing reads.
	// Closing its write end up front avoids both blocking on writes and racing
	// vt.Emulator.Read against Close on e.closed.
	if pw, ok := e.InputPipe().(io.Closer); ok {
		_ = pw.Close()
	}
	defer func() { _ = e.Close() }()
	_, _ = e.Write(out)
	var b strings.Builder
	for y := range rows {
		b.Reset()
		for x := range cols {
			if c := e.CellAt(x, y); c != nil && c.Content != "" {
				b.WriteString(c.Content)
			} else if c == nil || c.Width != 0 {
				b.WriteByte(' ')
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines, e.IsAltScreen()
}

// Type sends keys, as the user's terminal would.
func (term *Terminal) Type(keys string) {
	term.t.Helper()
	if _, err := term.pty.Write([]byte(keys)); err != nil {
		term.t.Fatal(err)
	}
}

// Resize changes the terminal's size. With signal, it also sends SIGWINCH to
// this process, as a terminal does to its foreground program.
func (term *Terminal) Resize(cols, rows int, signal bool) {
	term.t.Helper()
	if err := pty.Setsize(term.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		term.t.Fatal(err)
	}
	if signal {
		_ = syscall.Kill(os.Getpid(), syscall.SIGWINCH)
	}
}
