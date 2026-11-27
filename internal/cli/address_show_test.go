package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The text of show, from fixed data: a title, a line for each field with
// a value, in the order of the type, and the command that attaches, by
// host/ID.
func TestDescribeSession(t *testing.T) {
	old := showZone
	showZone = time.UTC
	t.Cleanup(func() { showZone = old })
	at := time.Date(2026, 11, 10, 10, 40, 0, 0, time.UTC)
	s := client.Session{ID: "abcd2345", Name: "auth-fix", Agent: "claude", Cwd: "/home/dev/src/app", State: "working", Source: "hooks",
		Since: at, CreatedAt: at, MemoryMax: 4 << 30, HostVersion: "0.2.0", Cols: 120, Rows: 36,
		Secrets: []string{"GITHUB_TOKEN", "NPM_TOKEN"}, MissingSecrets: []string{"NPM_TOKEN"}}
	got := describeSession("box", s)
	want := "auth-fix on box\n" +
		"  ID              abcd2345\n" +
		"  Agent           claude\n" +
		"  Folder          /home/dev/src/app\n" +
		"  State           working (from hooks)\n" +
		"  Since           10 Nov 2026 10:40\n" +
		"  Created         10 Nov 2026 10:40\n" +
		"  Memory cap      4.0 GiB\n" +
		"  Host helper     0.2.0\n" +
		"  Size            120×36\n" +
		"  Missing secrets NPM_TOKEN\n" +
		"  Secrets         GITHUB_TOKEN, NPM_TOKEN\n" +
		"  Attach          " + brand.Name + " attach box/abcd2345\n"
	if got != want {
		t.Errorf("show printed\n%s\nwant\n%s", got, want)
	}
	// An ended agent: the state says the exit code, so it has no line of its
	// own. The reason that the state does not say keeps its line, and so
	// does the signal.
	ended := client.Session{ID: "c3", Name: "dup", State: "errored", Source: "process", ExitCode: new(127),
		Reason: "fork/exec /no/such/agent: no such file or directory", Alive: true}
	got = describeSession("box", ended)
	if !strings.Contains(got, "State           errored (exit code 127, from process)\n") || strings.Contains(got, "Exit code") ||
		!strings.Contains(got, "Reason          fork/exec /no/such/agent: no such file or directory\n") ||
		!strings.Contains(got, "Session         still open") || !strings.HasSuffix(got, "attach box/c3\n") {
		t.Errorf("an ended agent:\n%s", got)
	}
	capped := client.Session{ID: "e5", Name: "big", State: "errored", OOM: true, Signal: "killed", ExitCode: new(137), Reason: "oom"}
	got = describeSession("box", capped)
	if !strings.Contains(got, "errored (out of memory)") || strings.Contains(got, "Reason") || !strings.Contains(got, "Signal          killed\n") ||
		!strings.Contains(got, "Exit code       137\n") {
		t.Errorf("an agent that the cap stopped:\n%s", got)
	}
	lost := client.Session{ID: "f6", Name: "gone", State: "lost", Reason: "the host restarted"}
	if got = describeSession("box", lost); strings.Contains(got, "Reason") {
		t.Errorf("a lost agent repeats its reason:\n%s", got)
	}
}
