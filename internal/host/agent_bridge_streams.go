package host

import (
	"context"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/link"
)

// This starts at first local revocation, overlaps client teardown, and
// never promises that an uninterruptible filesystem syscall has returned.
const agentProcessCleanupBudget = 6 * time.Second

func (b *hostAgentBridge) acceptAgents(endpoint *hostAgentEndpoint) {
	defer b.workers.Done()
	for {
		client, err := endpoint.listener.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed || !b.active {
			b.mu.Unlock()
			_ = client.Close()
			continue
		}
		select {
		case b.slots <- struct{}{}:
			b.clients[client] = struct{}{}
			b.workers.Add(1)
			b.mu.Unlock()
			go b.relayAgent(client)
		default:
			b.mu.Unlock()
			_ = client.Close()
		}
	}
}

func (b *hostAgentBridge) relayAgent(client net.Conn) {
	defer b.workers.Done()
	defer func() { <-b.slots }()
	defer func() {
		_ = client.Close()
		b.mu.Lock()
		delete(b.clients, client)
		b.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(b.ctx, time.Second)
	stream, err := b.link.Open(ctx, link.KindAgent, b.scope)
	cancel()
	if err != nil {
		return
	}
	_ = agentbridge.Relay(b.ctx, client, stream)
}

// revoke does no link join or filesystem-lock wait. Every live descriptor
// loses its capability before EndBridge waits for any cleanup.
func (b *hostAgentBridge) revoke() {
	b.once.Do(func() {
		if b.processOnly {
			b.cleanupCtx, b.stopCleanup = context.WithTimeout(context.WithoutCancel(b.ctx), agentProcessCleanupBudget)
		}
		b.cancel()
		if b.processOnly {
			b.link.AbortStreams()
		}
		_ = b.conn.Close()
		b.mu.Lock()
		b.closed, b.active = true, false
		endpoint := b.endpoint
		if b.probe != nil {
			_ = b.probe.Close()
		}
		for client := range b.clients {
			_ = client.Close()
		}
		b.mu.Unlock()
		endpoint.revoke()
	})
}

// A cleanup error is metadata only. The fixed process returns its distinct
// resource status after capabilities have already been revoked.
type agentProcessCleanupError struct{ cause error }

func (e *agentProcessCleanupError) Error() string {
	return "the agent bridge could not finish owned resource cleanup"
}
func (e *agentProcessCleanupError) Unwrap() error { return e.cause }

func (b *hostAgentBridge) finish() error {
	b.revoke()
	if !b.processOnly {
		_ = b.link.Close()
		b.workers.Wait()
		if b.endpoint != nil {
			return b.endpoint.Close()
		}
		return nil
	}
	// The single deadline began at FIRST revocation. It overlaps the
	// existing client teardown and is never restarted by a repeated close.
	finished := make(chan error, 1)
	go func() {
		b.workers.Wait()
		var err error
		if b.endpoint != nil {
			err = b.endpoint.CloseContext(b.cleanupCtx)
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		b.stopCleanup()
		return err
	case <-b.cleanupCtx.Done():
		return b.cleanupCtx.Err()
	}
}
