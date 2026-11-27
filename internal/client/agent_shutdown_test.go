//go:build darwin || linux

package client

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type delayedAgentFinalizer struct {
	pipe    net.Conn
	release chan struct{}
	killed  chan struct{}
	once    sync.Once
	failed  atomic.Bool
}

func (p *delayedAgentFinalizer) Stdin() io.WriteCloser { return p.pipe }
func (p *delayedAgentFinalizer) Stdout() io.Reader     { return p.pipe }
func (*delayedAgentFinalizer) Stderr() string          { return "" }
func (p *delayedAgentFinalizer) Kill()                 { p.once.Do(func() { close(p.killed) }) }
func (p *delayedAgentFinalizer) Wait() error {
	<-p.release
	p.failed.Store(true)
	return nil
}
func (p *delayedAgentFinalizer) agentFailure() error {
	if p.failed.Load() {
		return agentChannelError("the owned finalizer reported a delayed capture failure")
	}
	return nil
}

func TestAgentCloseFinalizesLateFailureBeforeReconnectClassification(t *testing.T) {
	agentClientEnv(t)
	local, peer := net.Pipe()
	process := &delayedAgentFinalizer{pipe: local, release: make(chan struct{}), killed: make(chan struct{})}
	connection := &Conn{agentOnly: true, proc: process, stdin: local, done: make(chan struct{}), procDone: make(chan struct{})}
	connection.watchProcess()
	release := sync.OnceFunc(func() { close(process.release) })
	defer func() { release(); connection.Close(); _ = peer.Close() }()
	finished := make(chan struct{})
	go func() { connection.Close(); close(finished) }()
	select {
	case <-process.killed:
	case <-time.After(time.Second):
		t.Fatal("agent close waited for diagnostics before revoking its owned process")
	}
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("agent close did not revoke owned transport before finalization")
	}
	// Three real seconds exceed the old two-second join. This is an
	// intentionally delayed finalizer, not a smaller substituted timeout.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-finished:
		t.Fatal("agent close returned before its late capture refusal was known")
	case <-timer.C:
		release()
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("agent close did not join the released finalizer within its composed budget")
	}
	if !genuineAgentRevocation(agentLinkError(connection, ErrClosed)) {
		t.Fatal("a late capture failure would enter ordinary automatic reconnect")
	}
	stopped := connection.agentStoppedAt
	connection.Close()
	_ = connection.closeAgentTransport()
	if !connection.agentStoppedAt.Equal(stopped) {
		t.Fatal("repeated cleanup reset the absolute shutdown budget")
	}
}
