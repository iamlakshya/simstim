//go:build unix

package cli

import (
	"strings"
	"testing"
)

// --debug alone turns on every category, and the word after it stays an
// argument. A list binds only in the = form. --debug-file names the file
// and wins over the variable, which turns on every category by itself.

// debug start, save, profile and stop work on a running session from
// another terminal, and refuse what they cannot do.
func TestTheDebugCommands(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "dbg", "/bin/sh", "-c", "exec sleep 60")
	out := okSaid(t, "debug", "start", "box/dbg", "--for", "10m", "--only", "memory,runtime")
	for _, want := range []string{"Debug is on for dbg on box until ", "(memory, runtime)", "~/.local/state/", "debug stop box/" + id + "\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("debug start lacks %q:\n%s", want, out)
		}
	}
	if ls := ok(t, "ls", "--json"); !strings.Contains(ls, `"debug_until"`) {
		t.Errorf("ls --json shows no end: %s", ls)
	}
	if out := okSaid(t, "debug", "save", "box/dbg"); !strings.Contains(out, "Saved the recent debug record of dbg: ~/") {
		t.Errorf("debug save = %q", out)
	}
	if out := okSaid(t, "debug", "profile", "box/dbg", "heap"); !strings.Contains(out, "Wrote a heap profile of dbg: ~/") {
		t.Errorf("debug profile = %q", out)
	}
	if out := okSaid(t, "debug", "profile", "box/dbg", "cpu", "--for", "1s"); !strings.Contains(out, "Wrote a cpu profile") {
		t.Errorf("debug profile cpu = %q", out)
	}
	if out := okSaid(t, "debug", "stop", "box/dbg"); !strings.HasPrefix(out, "Debug is off for dbg on box.") {
		t.Errorf("debug stop = %q", out)
	}
	if out := okSaid(t, "debug", "stop", "box/dbg"); out != "Debug was not on for dbg on box.\n" {
		t.Errorf("a second debug stop = %q", out)
	}
	fails(t, "--for is for a cpu profile only", "debug", "profile", "box/dbg", "heap", "--for", "1m")
	fails(t, "is not between 1s and 24h", "debug", "start", "box/dbg", "--for", "25h")
	failsWithHint(t, "net is not a category of the host", "--debug", "debug", "start", "box/dbg", "--only", "attach,net")
	fails(t, "no profile threads", "debug", "profile", "box/dbg", "threads")
	fails(t, "is not between 1s and 5m", "debug", "profile", "box/dbg", "cpu", "--for", "10m")
	failsWithHint(t, "the flight recorder of this session is off", "debug start", "debug", "save", "box/dbg")
}
