package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A command prints the line about its debug file before it starts. The
// launcher does not: its header shows the debug mark instead.
func TestNoDebugLineBeforeTheLauncher(t *testing.T) {
	newTestHome(t)
	root := Root()
	ls, _, err := root.Find([]string{"ls"})
	if err != nil {
		t.Fatal(err)
	}
	flip, _, err := root.Find([]string{"flip"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cmd  *cobra.Command
		args []string
		line bool
	}{{root, nil, false}, {flip, nil, false}, {flip, []string{"box/x"}, true}, {ls, nil, true}} {
		if err := c.cmd.ParseFlags([]string{"--debug"}); err != nil {
			t.Fatal(err)
		}
		got := stderrOf(t, func() {
			if err := startDebug(c.cmd, c.args); err != nil {
				t.Fatal(err)
			}
		})
		stopClientDebug(t)
		if strings.Contains(got, "debug on: ") != c.line {
			t.Errorf("%s %v printed %q", c.cmd.Name(), c.args, got)
		}
	}
}

// stderrOf is what fn writes to stderr.
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}
