//go:build darwin || linux

package client

import (
	"errors"
	"testing"
)

func TestLatchedAgentEOFDoesNotHideCompletedOwnedRefusal(t *testing.T) {
	process := &delayedAgentFinalizer{}
	agent := &attachedAgent{conn: &Conn{proc: process}, err: ErrClosed}
	if err := agent.Err(); !errors.Is(err, ErrClosed) || genuineAgentRevocation(err) {
		t.Fatalf("the incomplete observation invented a failure: %v", err)
	}
	process.failed.Store(true) // The owned finaliser has now completed.
	if err := agent.Err(); !genuineAgentRevocation(err) {
		t.Fatalf("a latched EOF hid the late failure and would reconnect: %v", err)
	}
	first := agentChannelError("an earlier protocol refusal")
	agent.err = first
	if agent.Err() != first {
		t.Fatal("finalisation replaced the original genuine refusal")
	}
}
