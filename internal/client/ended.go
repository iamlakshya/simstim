package client

import "github.com/iamlakshya/simstim/internal/proto"

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
