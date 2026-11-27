package client

import "github.com/iamlakshya/simstim/internal/agentbridge"

// Only the fixed agent process owns these portable statuses. Transport,
// shell and signal failures retain their ordinary classification.
func agentProcessExitFailure(code int) error {
	switch code {
	case agentbridge.ProcessExitRefused:
		return agentChannelError("the host refused the agent bridge's process protocol")
	case agentbridge.ProcessExitCleanup:
		return agentChannelError("the host's agent bridge could not finish its owned resource cleanup")
	default:
		return nil
	}
}
