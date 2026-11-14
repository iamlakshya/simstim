package client

import (
	"context"
	"os"
	"sync"
	"time"
)

var stageAbortWait = 3 * time.Second

type stageBinding struct {
	name string
	st   *stage
	prev *stageBinding
}

type stageBindingKey struct{}

// StagePinOwned opens an operation-owned stage. The returned context binds
// only this host to that stage. Its idempotent abort never removes a stage
// opened by another operation, even when it replaced this one.
func StagePinOwned(ctx context.Context, name string) (context.Context, func(), error) {
	st, err := openPinStage(ctx, name)
	if err != nil || st == nil {
		return ctx, func() {}, err
	}
	prev, _ := ctx.Value(stageBindingKey{}).(*stageBinding)
	bound := context.WithValue(ctx, stageBindingKey{}, &stageBinding{name: name, st: st, prev: prev})
	var once sync.Once
	return bound, func() { once.Do(func() { abortPinStage(ctx, name, st) }) }, nil
}

func stageForContext(ctx context.Context, name string) (*stage, error) {
	for binding, _ := ctx.Value(stageBindingKey{}).(*stageBinding); binding != nil; binding = binding.prev {
		if binding.name == name {
			return binding.st, sameStage(name, binding.st)
		}
	}
	st := currentStage(name)
	return st, sameStage(name, st)
}

func abortPinStage(ctx context.Context, name string, st *stage) {
	if st == nil {
		return
	}
	stagesMu.Lock()
	st.closed = true
	current := stages[name] == st
	stagesMu.Unlock()
	if !current {
		_ = os.Remove(st.path)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stageAbortWait)
	defer cancel()
	unlock, err := lockPin(ctx, name)
	if err != nil {
		DebugLog().Warn("the host key stage lock did not close in time", "host", name, "err", err)
		// It is already closed. Only this operation owns this temporary
		// path. A writer that passed its check removes it after writing.
		finishPinStageLocked(name, st)
		return
	}
	defer unlock()
	finishPinStageLocked(name, st)
}

func finishPinStageLocked(name string, st *stage) {
	if st == nil {
		return
	}
	stagesMu.Lock()
	st.closed = true
	if stages[name] == st {
		delete(stages, name)
	}
	stagesMu.Unlock()
	_ = os.Remove(st.path)
}
