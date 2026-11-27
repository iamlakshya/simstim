//go:build unix

package cli

import (
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestColourOn(t *testing.T) {
	tty := hosttest.NewTerminal(t, 80, 24).TTY
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for _, c := range []struct {
		f             *os.File
		noColor, term string
		want          bool
	}{
		{tty, "", "xterm-256color", true},
		{w, "", "xterm-256color", false},
		{tty, "1", "xterm-256color", false},
		{tty, "", "dumb", false},
	} {
		t.Setenv("NO_COLOR", c.noColor)
		t.Setenv("TERM", c.term)
		if got := colourOn(c.f); got != c.want {
			t.Errorf("colourOn(tty %v, NO_COLOR=%q, TERM=%q) = %v", c.f == tty, c.noColor, c.term, got)
		}
	}
}
