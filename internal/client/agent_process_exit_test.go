package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func TestAgentProcessExitStatusesRemainDistinctAndPortable(t *testing.T) {
	for _, code := range []int{-1, 0, 1, 2, 64, 73, 74, 75, 76, 125, 126, 127, 128, 141, 255} {
		err := agentProcessExitFailure(code)
		want := code == agentbridge.ProcessExitRefused || code == agentbridge.ProcessExitCleanup
		if genuineAgentRevocation(err) != want {
			t.Fatalf("exit %d classified as an agent refusal: %v", code, err)
		}
	}
	if agentProcessExitFailure(agentbridge.ProcessExitRefused).Error() == agentProcessExitFailure(agentbridge.ProcessExitCleanup).Error() {
		t.Fatal("protocol and incomplete cleanup lost their distinct diagnostics")
	}
}
