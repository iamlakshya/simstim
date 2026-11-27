package client

import (
	"context"
	"sync"
	"time"
)

// One timestamp spans first local revocation, raw reaping, diagnostic scan,
// lease/endpoint cleanup and the retained mode directory. Retries add no grace.
type agentShutdownBudget struct {
	mu    sync.Mutex
	since time.Time
}

func (b *agentShutdownBudget) start(at time.Time) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.since.IsZero() {
		b.since = at
	}
	return b.since
}
func (b *agentShutdownBudget) deadline() time.Time { return b.start(time.Now()).Add(3 * sshWaitDelay) }
func (b *agentShutdownBudget) context(parent context.Context, phase time.Duration) (context.Context, context.CancelFunc) {
	deadline := b.deadline()
	if phase > 0 {
		if limit := time.Now().Add(phase); limit.Before(deadline) {
			deadline = limit
		}
	}
	return context.WithDeadline(parent, deadline)
}
func (l *agentNativeLog) shutdownBudget() *agentShutdownBudget {
	if l.mode != nil {
		return &l.mode.shutdown
	}
	return &l.shutdown
}
func (p *sshProc) revokeAgentAt(at time.Time) time.Time {
	if p.logFile != nil && p.logFile.agent != nil {
		return p.logFile.agent.shutdownBudget().start(at)
	}
	return at
}

// Kill revokes the actual owned process's budget before sending its signal.
func (p *sshProc) Kill() { p.revokeAgentAt(time.Now()); p.Proc.Kill() }
