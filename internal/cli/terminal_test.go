package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// inTerminal runs the command line with a terminal as its stdin and stdout,
// in the background. It returns the terminal and a channel for the error.
func inTerminal(t *testing.T, args ...string) (*hosttest.Terminal, chan error) {
	t.Helper()
	term := hosttest.NewTerminal(t, 90, 30)
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = term.TTY, term.TTY
	done := make(chan error, 1)
	go func() {
		root := Root()
		root.SetArgs(args)
		done <- root.ExecuteContext(context.Background())
	}()
	t.Cleanup(func() { os.Stdin, os.Stdout = oldIn, oldOut })
	return term, done
}

func wait(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("the command did not end")
		return nil
	}
}

func TestNewAttachesAndDetaches(t *testing.T) {
	newTestHost(t)
	term, done := inTerminal(t, "new", "box", "--name", "live", "--", "/bin/sh", "-c", "stty size; exec sleep 60")
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

// An end with a known next step says it, as an error does (AGENTS.md rule 7).
func TestEndMessage(t *testing.T) {
	for _, c := range []struct {
		e    proto.Ended
		want string
	}{
		{proto.Ended{ExitCode: 9}, "\r\nThe session app on box ended (exit code 9).\n"},
		{proto.Ended{ExitCode: 137, Signal: "killed"}, "\r\nThe session app on box ended (killed).\n"},
		{proto.Ended{ExitCode: -1}, "\r\nThe session app on box stopped.\n"},
		{proto.Ended{ExitCode: 137, Signal: "killed", OOM: true},
			"\r\nThe session app on box ended (out of memory).\n\nTry: " + client.EndHint(&proto.Ended{OOM: true}, "box/a1") + "\n"},
	} {
		if got := endMessage("app", "box", "a1", &c.e); got != c.want {
			t.Errorf("%+v: %q, want %q", c.e, got, c.want)
		}
	}
	if client.EndHint(&proto.Ended{OOM: true}, "") == "" || client.EndHint(&proto.Ended{ExitCode: 1}, "box/a1") != "" || client.EndHint(nil, "") != "" {
		t.Error("only an end out of memory has a next step")
	}
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

func TestTerminalCommandsNeedATerminal(t *testing.T) {
	newTestHost(t)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	fails(t, "needs a terminal", "doctor", "keys")
	fails(t, "needs a terminal", "doctor", "terminal")
}
