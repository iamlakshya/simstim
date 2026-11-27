//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/paths"
)

type rawExitAgentTransport struct {
	channelTransport
	raw, final chan struct{}
}

func (t *rawExitAgentTransport) Start(ctx context.Context, remote string) (Proc, error) {
	p, err := t.channelTransport.Start(ctx, remote)
	if err != nil {
		return nil, err
	}
	return &rawExitAgentProc{Proc: p, raw: t.raw, final: t.final}, nil
}

// The earlier real owned-child proof establishes the raw-exit contract.
// This fixture deliberately leaves the transport and diagnostics live to
// isolate the production watcher's revocation of an already active stream.
type rawExitAgentProc struct {
	Proc
	raw, final chan struct{}
}

func (p *rawExitAgentProc) rawExited() <-chan struct{} { return p.raw }
func (p *rawExitAgentProc) Wait() error {
	err := p.Proc.Wait()
	<-p.final
	return err
}

func TestAgentRawExitRevokesExistingStreamsBeforeTransportAndDiagnostics(t *testing.T) {
	h, scope, source := channelHostFixture(t)
	transport := &rawExitAgentTransport{raw: make(chan struct{}), final: make(chan struct{})}
	release := sync.OnceFunc(func() { close(transport.final) })
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := dial(ctx, h, route{t: transport, agentOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); conn.Close() }()
	mode := agentModeForTest(t)
	parent := generatedAgentChannelParent(t, ctx, h, conn)
	a, err := bindAgentConn(ctx, conn, scope, source.path, nil, mode, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); a.Close() }()
	// Preverified fixture token. This does not claim native provenance.
	a.binding = fwBinding()
	a.binding.owner, a.binding.scope = conn, scope
	if err := a.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	connection := signChannel(t, paths.AgentSocket(scope.Session), source.public)
	defer connection.Close()
	accepts, lists, signs := source.accepts.Load(), source.agent.lists.Load(), source.agent.signs.Load()
	select {
	case <-conn.Done():
		t.Fatal("fixture transport ended before its isolated raw-exit event")
	default:
	}
	close(transport.raw)
	select {
	case <-a.Done():
	case <-time.After(time.Second):
		t.Fatal("raw exit did not revoke the active capability before diagnostics")
	}
	select {
	case <-conn.procDone:
		t.Fatal("fixture did not retain unfinished diagnostics")
	default:
	}
	if a.isActive() || a.binding.matches(conn, scope) {
		t.Fatal("an exited owner retained active binding authority")
	}
	_, err = agent.NewClient(connection).List()
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("an existing agent stream served or hung after raw exit: %v", err)
	}
	if source.accepts.Load() != accepts || source.agent.lists.Load() != lists || source.agent.signs.Load() != signs {
		t.Fatal("a source request reached the agent after raw exit")
	}
	release()
	a.Close()
	if err := a.Activate(ctx); err == nil {
		t.Fatal("an exited owner reactivated its old lease")
	}
}
