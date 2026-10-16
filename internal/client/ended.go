package client

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// An agent that has ended: how its session tells, and the step to try. The
// launcher, ls and attach all use these, so they agree.

// EndHint is the step to try after a session's agent ended as e says, or ""
// when there is none. After the memory cap stopped the agent, the step is
// a resume of ref, the session as host/session. The resume starts the agent
// again in the same session, where it can pick up its conversation, and
// can give it a bigger cap. The launcher says the same with its own key
// (endHint in internal/tui).
func EndHint(e *proto.Ended, ref string) string {
	if e == nil || !e.OOM {
		return ""
	}
	return brand.Name + " resume " + ref + "   (--memory-max <size> gives it a bigger cap)"
}

// SessionEndHint is EndHint for a listed session whose agent has ended: the
// same step after the memory cap stopped it, or "".
func SessionEndHint(s Session, ref string) string {
	return EndHint(&proto.Ended{OOM: CapStopped(s)}, ref)
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

// CleanExit reports whether a session's agent exited by itself with code 0,
// as when the user quits it.
func CleanExit(s Session) bool {
	return s.State == api.StateExited && s.Signal == "" && (s.ExitCode == nil || *s.ExitCode == 0)
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
