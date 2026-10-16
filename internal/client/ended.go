package client

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// An agent that has ended: how its session tells, and the step to try. The
// launcher, ls and attach all use these, so they agree.

// EndHint is the step to try after a session's agent ended as e says, or ""
// when there is none. The command line and the launcher both show it. Keep
// it short. The launcher shows two lines of notices. So the end and its step
// both show only when the step fits on one line of a 60-column terminal.
func EndHint(e *proto.Ended) string {
	if e != nil && e.OOM {
		// The memory cap stopped the agent. A new session in the same folder
		// starts it again, and an agent with a resume command can then pick
		// up its conversation.
		return "Run new in its folder. Resume if the agent can."
	}
	return ""
}

// SessionEndHint is EndHint for a listed session whose agent has ended: the
// same step after the memory cap stopped it, or "".
func SessionEndHint(s Session) string {
	return EndHint(&proto.Ended{OOM: CapStopped(s)})
}

// Ended reports whether a session's agent has ended: it exited, it failed,
// or its holder was lost. Alive says only that the session's holder
// answers, and a holder outlives its agent.
func Ended(s Session) bool {
	switch s.State {
	case api.StateExited, api.StateErrored, api.StateLost:
		return true
	default:
		return false
	}
}

// CapStopped reports whether the memory cap stopped the session's agent
// itself. While the agent runs, the out-of-memory mark means that the cap
// stopped another process in the session. An older holder sets the mark
// again after its agent exits, so on an ended session the mark counts only
// with SIGKILL, the signal that the kernel's out-of-memory killer sends.
func CapStopped(s Session) bool {
	return Ended(s) && s.OOM && IsKill(s.Signal)
}

// ChildStopped reports whether the memory cap stopped another process in
// the session, not the agent. While the agent runs, the out-of-memory mark
// says so. After it ends, so does the mark without the agent's own stop (an
// older holder), or the reason api.ReasonChildOOM without the mark (a
// newer holder, when the kill came just before the exit).
func ChildStopped(s Session) bool {
	if !Ended(s) {
		return s.OOM
	}
	return !CapStopped(s) && (s.OOM || s.Reason == api.ReasonChildOOM)
}

// IsKill reports whether sig, as a host wrote it, is SIGKILL: Go's name
// for it, "killed", or an older form such as "SIGKILL".
func IsKill(sig string) bool {
	switch strings.TrimPrefix(strings.ToUpper(sig), "SIG") {
	case "KILLED", "KILL":
		return true
	default:
		return false
	}
}
