package client

import (
	"context"
	"sync"
)

type hostMutationContextKey struct{}

type hostMutationScope struct {
	mu      sync.Mutex
	active  bool
	name    string
	op      *heldLock
	ctx     context.Context
	workers sync.WaitGroup
}

// holdHostPublicationContext pins the ACTUAL OP through the first publication.
// Its caller must release this shared participant BEFORE calling OP unlock.
// It holds no membership lease across a pipe write, flush or response wait.
func holdHostPublicationContext(ctx context.Context, name string) (context.Context, func(), error) {
	if ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostMembershipContextKey{}) != nil {
		return ctx, nil, hostStateRemovalError("publication cannot begin inside metadata mutation or cleanup")
	}
	held, _ := ctx.Value(heldKey{name}).(*heldLock)
	if held == nil {
		return ctx, nil, hostStateRemovalError("publication has no actual host operation")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return ctx, nil, err
	}
	if held.terminal.Load() {
		return ctx, nil, hostStateRemovalError("publication cannot begin on a retiring OP")
	}
	lifetime := held.stateLifetime()
	leave, err := lifetime.enter(ctx)
	if err != nil {
		return ctx, nil, err
	}
	// The ordinary RPC waits for its response AFTER first-publication release.
	// Do not cancel that caller context when the shared OP participant ends.
	// Local state transactions derive their own operation-cancelled contexts.
	return ctx, sync.OnceFunc(leave), nil
}

// holdHostMutationContext is the canonical owned mutator. It is not a bulk
// probe bypass based on a held context. The actual OP's exclusive gate drains
// same-owner publications before pin/membership/tier work can begin.
func holdHostMutationContext(ctx context.Context, name string) (context.Context, func(), error) {
	held, _ := ctx.Value(heldKey{name}).(*heldLock)
	if held == nil {
		return ctx, nil, hostStateRemovalError("metadata mutation has no actual host operation")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return ctx, nil, err
	}
	if held.terminal.Load() {
		return ctx, nil, hostStateRemovalError("metadata mutation cannot begin on a retiring OP")
	}
	if existing, _ := ctx.Value(hostMutationContextKey{}).(*hostMutationScope); existing != nil {
		if existing.name != name || existing.op != held {
			return ctx, nil, hostStateRemovalError("the metadata mutation belongs to another operation")
		}
		if err := existing.check(); err != nil {
			return ctx, nil, err
		}
		return ctx, func() {}, nil
	}
	if ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil ||
		ctx.Value(hostStateInspectionKey{}) != nil || ctx.Value(hostTerminalInspectionKey{}) != nil {
		return ctx, nil, hostStateRemovalError("metadata mutation cannot begin inside a membership callback or owner inspection")
	}
	lifetime := held.stateLifetime()
	end, err := lifetime.mutate(ctx)
	if err != nil {
		return ctx, nil, err
	}
	bound, stop := lifetime.transactionContext(ctx)
	scope := &hostMutationScope{active: true, name: name, op: held, ctx: bound}
	bound = context.WithValue(bound, hostMutationContextKey{}, scope)
	return bound, sync.OnceFunc(func() {
		scope.mu.Lock()
		scope.active = false
		scope.mu.Unlock()
		stop()
		scope.workers.Wait()
		end()
	}), nil
}

func (s *hostMutationScope) check() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.op.gone.Load() {
		return hostStateRemovalError("the actual metadata mutation scope ended")
	}
	return s.ctx.Err()
}

func (s *hostMutationScope) enter(ctx context.Context, name string, held *heldLock) (context.Context, func(), error) {
	s.mu.Lock()
	if !s.active || s.name != name || s.op != held || held.gone.Load() {
		s.mu.Unlock()
		return ctx, nil, hostStateRemovalError("state preparation has no actual live metadata mutation scope")
	}
	s.workers.Add(1)
	s.mu.Unlock()
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return bound, sync.OnceFunc(func() { stop(); cancel(); s.workers.Done() }), nil
}
