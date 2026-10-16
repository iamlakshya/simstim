package cli

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Sessions whose agent has ended, as ls and attach show them. The rule that
// tells them apart is in internal/client/ended.go, which the launcher uses
// too.

// stateLabel is a session's state as ls prints it, with why its agent
// ended: "errored (out of memory)", "errored (killed)".
func stateLabel(s client.Session) string {
	st := strings.ReplaceAll(s.State, "_", " ")
	if why := endCause(s); why != "" {
		return st + " (" + why + ")"
	}
	return st
}

// endCause is why a session's agent ended, or "" while it runs or when it
// exited with code 0. The host marks every non-zero exit "errored", so the
// cause comes from the out-of-memory mark, the signal and the exit code.
// When the cap stopped another process in the session, the cause says so
// too: "exit code 1, a process hit the cap".
func endCause(s client.Session) string {
	if !client.Ended(s) {
		return ""
	}
	var why string
	switch {
	case s.State == api.StateLost:
		why = s.Reason
	case client.CapStopped(s):
		why = "out of memory"
	case s.Signal != "":
		why = s.Signal
	case s.ExitCode != nil && *s.ExitCode != 0:
		why = fmt.Sprintf("exit code %d", *s.ExitCode)
	}
	if client.ChildStopped(s) {
		why = strings.TrimPrefix(why+", a process hit the cap", ", ")
	}
	return why
}

// endedError is the error for an attach to a session whose agent has
// ended. It reads like the message at the end of an attach (endMessage),
// with the same step after the memory cap (client.EndHint). Otherwise the
// step brings the agent back, or clears the session away.
func endedError(h client.Host, s client.Session) error {
	why := endCause(s)
	if why == "" && s.ExitCode != nil {
		why = fmt.Sprintf("exit code %d", *s.ExitCode)
	}
	msg := fmt.Sprintf("the session %s on %s ended", s.Name, h.Name)
	if why != "" {
		msg += " (" + why + ")"
	}
	hint := client.SessionEndHint(s)
	switch {
	case s.State == api.StateLost:
		// Restore starts the agent again with its resume command.
		hint = fmt.Sprintf("%s host restore %s --all", brand.Name, h.Name)
	case hint == "":
		hint = fmt.Sprintf("%s kill %s/%s, then start a new one", brand.Name, h.Name, s.ID)
	}
	return &client.UserError{Msg: msg, Hint: hint}
}
