//go:build unix

package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// An actual terminal makes the control cases yellow. no-color must still
// suppress it before the hook, while false/value/positional forms do not.
func TestEarlyOldWarningsRespectColour(t *testing.T) {
	withFake(t, nil)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	for _, tt := range []struct {
		args   []string
		colour bool
	}{
		{[]string{"-v"}, true},
		{[]string{"-v", "--no-color"}, false},
		{[]string{"-v", "--no-color=false"}, true},
		{[]string{"-v", "--", "--no-color"}, true},
		{[]string{"new", "my-vps", "-d", "/srv", "--badflag", "--no-color"}, false},
		{[]string{"host", "alerts", "log", "my-vps", "extra", "-n", "5", "--no-color"}, false},
		{[]string{"new", "my-vps", "-d", "--no-color", "--badflag"}, true},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			term := hosttest.NewTerminal(t, 100, 10)
			cmd, _ := earlyFlagCommand(t, term.TTY, tt.args)
			warnOldFlags(cmd)
			fmt.Fprintln(term.TTY, "warning-end")
			out := string(term.WaitFor("warning-end"))
			if !strings.Contains(out, "Warning: ") || strings.Contains(out, "\x1b[33m") != tt.colour {
				t.Fatalf("warning colour = %q, want colour %v", out, tt.colour)
			}
		})
	}
}
