package cli

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/bundle"
)

// The bundle never replaces the words of the command line, or a session
// called like a command would turn them into placeholders, in doctor's
// output too. Each command, and each of its aliases, must be among the
// words that the bundle keeps: a new command needs its word there.
func TestTheBundleKeepsEveryCommandsWord(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			for _, w := range append([]string{sub.Name()}, sub.Aliases...) {
				if !bundle.Keeps(w) {
					t.Errorf("the bundle would replace %q, the word of %q: add it to productWords in internal/bundle", w, sub.CommandPath())
				}
			}
			walk(sub)
		}
	}
	walk(Root())
}
