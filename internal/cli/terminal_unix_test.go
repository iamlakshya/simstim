//go:build unix

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// inTerminal runs the command line with a terminal as its stdin, stdout and
// stderr, in the background, so input is on. It returns the terminal and a
// channel for the error.
func inTerminal(t *testing.T, args ...string) (*hosttest.Terminal, chan error) {
	t.Helper()
	term := hosttest.NewTerminal(t, 90, 30)
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = term.TTY, term.TTY, term.TTY
	root := Root()
	root.SetArgs(args)
	done, stop := runInBackground(root, args)
	t.Cleanup(func() {
		stop()
		os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	})
	return term, done
}
func TestNewAttachesAndDetaches(t *testing.T) {
	newTestHost(t)
	term, done := inTerminal(t, "new", "box", "--wall", "none", "--downgrade", "--name", "live", "--", "/bin/sh", "-c", "stty size; exec sleep 60")
	term.WaitFor("30 90") // the session got this terminal's size
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("Detached. live keeps running on box.")

	// attach - finds it again.
	term2, done := inTerminal(t, "attach", "-")
	term2.WaitFor("\x1b[22;0t")
	term2.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term2.WaitFor("Detached. live keeps running on box.")
}
func TestAttachReportsTheEnd(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "short", "/bin/sh", "-c", "stty raw; head -c 1 >/dev/null; exit 9")
	term, done := inTerminal(t, "attach", "box/"+id)
	term.WaitFor("\x1b[22;0t")
	term.Type("x")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("The session short on box ended (exit code 9).")

	id = newSession(t, "killed", "/bin/sh", "-c", "stty raw; head -c 1 >/dev/null; kill -TERM $$")
	term, done = inTerminal(t, "attach", "box/"+id)
	term.WaitFor("\x1b[22;0t")
	term.Type("x")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("The session killed on box ended (terminated).")
}
func TestKeyTester(t *testing.T) {
	newTestHost(t)
	term, done := inTerminal(t, "doctor", "keys")
	term.WaitFor("Press keys.")
	term.Type("a")
	term.WaitFor(`"a"`)
	term.Type("\x1c")
	term.WaitFor(`"\x1c"  ← detach key`)
	term.Type("\x1b[200~hi\x1b[201~")
	term.WaitFor("← a paste")
	term.Type("q")
	term.WaitFor(`"q"`)
	term.Type("q")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if out := term.WaitFor("\x1b[?2004l"); !bytes.HasPrefix(out, []byte("\x1b[>31u")) || !bytes.HasSuffix(out, []byte("\"q\"\r\n\"q\"\r\n\x1b[<u\x1b[?2004l")) {
		t.Errorf("the kitty keyboard protocol was not turned on and off: %q", out)
	}
}
func TestTerminalProbe(t *testing.T) {
	newTestHost(t)
	t.Setenv("COLORTERM", "truecolor")
	term, done := inTerminal(t, "doctor", "terminal")
	term.WaitFor("\x1b[c")
	// The answers of a modern terminal, ending with DA1.
	term.Type("\x1bP>|TestTerm 1.2\x1b\\\x1b[?1u\x1b[?2026;2$y\x1b[?2027;4$y\x1b[?2031;0$y\x1b[?2004;1$y\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\\x1b[?62;22c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	out := string(term.WaitFor("TERM"))
	for _, want := range []string{
		"✓ terminal                   TestTerm 1.2",
		"✓ kitty keyboard protocol    supported",
		"✓ synchronized output        supported",
		"✓ grapheme clustering        always on or off",
		"✕ colour-scheme reports      not supported",
		"✕ focus reports              no answer",
		"✓ background colour          rgb:1e1e/1e1e/2e2e",
		"✓ truecolour                 COLORTERM=truecolor",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
}
