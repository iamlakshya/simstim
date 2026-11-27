package cli

import (
	"strings"
	"testing"
)

// The fourth acceptance line of C5: the top level holds only the commands
// of cli-spec 2.1. Hidden commands, such as kill and flip, and help topics
// are left out (cli-spec 2.3).
func TestTopLevel(t *testing.T) {
	allowed := map[string]bool{}
	for _, c := range strings.Fields(
		"new attach resume ls show rm rename send ports forward pull " + // sessions
			"stop shell exec cp fork rewind prune events " + // sessions, from the isolation plan
			"host policy image agents " +
			"setup doctor debug config demo version completion help ssh-config ssh-proxy") {
		allowed[c] = true
	}
	root := Root()
	for _, c := range root.Commands() {
		if !c.Hidden && !c.IsAdditionalHelpTopicCommand() && !allowed[c.Name()] {
			t.Errorf("%q is at the top level, and cli-spec 2.1 does not list it", c.Name())
		}
	}
	for _, want := range []string{"kill", "flip"} {
		c, _, err := root.Find([]string{want})
		if err != nil || c == root || !c.Hidden {
			t.Errorf("%s is not a hidden command", want)
		}
	}
}
