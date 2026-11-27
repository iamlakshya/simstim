package client

import (
	"context"
	"testing"
)

func agentModeForTest(t *testing.T) *agentModeResources {
	t.Helper()
	mode, err := newAgentModeResources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), sshWaitDelay)
		defer cancel()
		if err := mode.close(ctx); err != nil {
			t.Error(err)
		}
	})
	return mode
}
