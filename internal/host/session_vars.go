package host

import (
	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
)

// sessionVars are the variables that every session's agent gets, with or
// without a wall: the terminal's identity, the session's ID, the hook
// socket hook, and the variables of the agent's inline mode. It also
// returns the TERM it chose.
func sessionVars(a NewArgs, id, hook string, agent agents.Agent, inline bool) (vars map[string]string, term string) {
	term = a.Term
	if !HasTerminfo(term) {
		term = "xterm-256color"
	}
	colorterm := a.ColorTerm
	if colorterm == "" {
		colorterm = "truecolor"
	}
	vars = map[string]string{
		"TERM": term, "COLORTERM": colorterm,
		"TERM_PROGRAM": a.TermProgram, "TERM_PROGRAM_VERSION": a.TermProgramVersion,
		brand.Env("SESSION"): id, brand.Env("HOOK_SOCK"): hook,
	}
	if inline {
		for k, v := range agent.Inline.Env {
			vars[k] = v
		}
	}
	return vars, term
}
