package client

import (
	"context"
	"sync"
)

// Cached work uses this owned context, never the later RPC response context.
// Err/Done synchronously observe mode revocation as well as the transaction.
type agentInspectionContext struct {
	context.Context
	mode   context.Context
	cancel context.CancelFunc
}

func (c *agentInspectionContext) Err() error {
	if c.mode.Err() != nil {
		c.cancel()
	}
	return c.Context.Err()
}

func (c *agentInspectionContext) Done() <-chan struct{} {
	if c.mode.Err() != nil {
		c.cancel()
	}
	return c.Context.Done()
}

type agentInspectionCancellation struct {
	cancel context.CancelFunc
	stop   func() bool
	done   chan struct{}
	once   sync.Once
}

func newAgentInspectionContext(parent, mode context.Context) (context.Context, *agentInspectionCancellation, error) {
	if parent == nil || mode == nil {
		return nil, nil, agentModeResourceError()
	}
	// Actual mode lifetimes have cancellation only. The caller/transaction
	// retains the existing deadline; this helper adds no grace or timeout.
	base, cancel := context.WithCancel(parent)
	control := &agentInspectionCancellation{cancel: cancel, done: make(chan struct{})}
	control.stop = context.AfterFunc(mode, func() {
		defer close(control.done)
		cancel()
	})
	return &agentInspectionContext{Context: base, mode: mode, cancel: cancel}, control, nil
}

// finish joins only the owned cancellation callback. Cached work must have
// stopped first; it runs before region/lease cleanup, including early errors.
// Err/Done still synchronously observe mode after this callback is stopped.
func (c *agentInspectionCancellation) finish() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		if !c.stop() {
			<-c.done
		}
	})
}

func (c *agentInspectionCancellation) close() {
	if c != nil {
		c.finish()
		c.cancel()
	}
}
