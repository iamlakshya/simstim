//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

type agentMemoryClientCycle struct {
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	actor              *attachedAgent
	err                error
	prepared, finished chan struct{}
}

func startAgentMemoryClientCycle(parent context.Context, cfg agentMemoryConfig, cycle int, control *agentMemoryControl) *agentMemoryClientCycle {
	ctx, cancel := context.WithCancel(parent)
	op := &agentMemoryClientCycle{ctx: ctx, cancel: cancel, prepared: make(chan struct{}), finished: make(chan struct{})}
	go func() {
		defer close(op.finished)
		h, err := FindHost(cfg.HostNames[cycle])
		var actor *attachedAgent
		if err == nil {
			probe := &Conn{Host: h, hostID: cfg.HostID, Features: []string{agentbridge.Feature}, route: route{t: &SSH{Host: h}}}
			actor, err = prepareAttachedAgent(ctx, probe, "abc23def", false)
		}
		op.mu.Lock()
		op.actor, op.err = actor, err
		op.mu.Unlock()
		close(op.prepared)
		e := agentMemoryEvent{Role: "client", Event: "prepared", Phase: fmt.Sprintf("cycle-%d", cycle)}
		if err != nil {
			e.Error = err.Error()
			var refusal *UserError
			e.Refused = errors.As(err, &refusal) && refusal.Code == "ssh_agent_refused" && refusal.Kind == KindRefused
		}
		_ = control.send(e)
	}()
	return op
}

func (op *agentMemoryClientCycle) ready() (*attachedAgent, error) {
	<-op.prepared
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.actor, op.err
}

func (op *agentMemoryClientCycle) close() {
	op.cancel()
	actor, _ := op.ready()
	if actor != nil {
		actor.Close()
	}
	<-op.finished
	op.mu.Lock()
	op.actor = nil
	op.mu.Unlock()
}
