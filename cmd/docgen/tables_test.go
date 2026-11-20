package main

import (
	"strings"
	"testing"
)

// The agents page says how a session comes back without a usable ID: with
// its own command when it has one, else with the agent's continue command.
// It also explains the forms of the rules in its table.
func TestAgentsPageSaysWhatComesBack(t *testing.T) {
	page := string(agentsPage())
	for _, want := range []string{
		"Without a usable ID, a session with a command of its own runs that command again. Any other session runs the agent's continue command.",
		"unless the session's own command holds the same argument, with the same values.",
		"`--flag=...` takes a list of values",
		"`key!=a|b` is that key with any value but these",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the agents page lacks %q", want)
		}
	}
}
