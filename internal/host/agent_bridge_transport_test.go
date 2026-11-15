package host

import (
	"context"
	"io"
	"testing"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

type unprovedAgentTransport struct{ reads, writes int }

func (t *unprovedAgentTransport) Read([]byte) (int, error)    { t.reads++; return 0, io.EOF }
func (t *unprovedAgentTransport) Write(p []byte) (int, error) { t.writes++; return len(p), nil }
func (*unprovedAgentTransport) Close() error                  { return nil }

func TestAgentLibraryCloserAloneCannotStartAnIdentityRead(t *testing.T) {
	transport := &unprovedAgentTransport{}
	if err := RunAgentBridge(transport, transport); err == nil {
		t.Fatal("an arbitrary closer acquired the interruptible library transport contract")
	}
	if transport.reads != 0 || transport.writes != 0 {
		t.Fatal("unproved transport reached identity bytes")
	}
}

func TestAgentProcessCancelledOwnerReadsNoBytes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	transport := &unprovedAgentTransport{}
	if code := RunAgentBridgeProcessContext(ctx, transport, transport); code != 0 {
		t.Fatal("cancelled process owner did not end normally", code)
	}
	if transport.reads != 0 || transport.writes != 0 {
		t.Fatal("cancelled process owner started raw IO")
	}
	if code := RunAgentBridgeProcessContext(nil, transport, transport); code != agentbridge.ProcessExitRefused {
		t.Fatal("missing process owner did not fail closed", code)
	}
}
