package client

import (
	"context"
	"sync"
)

type hostMembershipContextKey struct{}

// hostMembershipContext is one actual canonical hosts.toml lock lifetime.
// Its context is not authority after stop, including through WithoutCancel.
type hostMembershipContext struct {
	mu      sync.Mutex
	active  bool
	workers sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

func heldHostMembershipContext(ctx context.Context) (context.Context, func()) {
	owned, cancel := context.WithCancel(ctx)
	scope := &hostMembershipContext{active: true, ctx: owned, cancel: cancel}
	return context.WithValue(owned, hostMembershipContextKey{}, scope), scope.stop
}

func (s *hostMembershipContext) enter() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return nil, hostStateRemovalError("the canonical host membership lease ended")
	}
	s.workers.Add(1)
	return sync.OnceFunc(s.workers.Done), nil
}

func (s *hostMembershipContext) check() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return hostStateRemovalError("the canonical host membership lease ended")
	}
	return s.ctx.Err()
}

func (s *hostMembershipContext) stop() {
	s.mu.Lock()
	s.active = false
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
}

// acquireHostMembershipContext is the only new membership acquisition wrapper.
// It always delegates the native lock to existing lockHostsFile. Reentrant
// users are registered and cannot outlive physical unlock.
func acquireHostMembershipContext(ctx context.Context) (context.Context, func(), error) {
	return acquireHostMembershipContextMode(ctx, true)
}

// Bulk preserves its existing parent operation deadline. The native lock wait
// remains bounded inside lockHostsFile. No per-name or host-count budget is
// substituted for the large canonical bulk operation.
func acquireHostMembershipWriteContext(ctx context.Context) (context.Context, func(), error) {
	return acquireHostMembershipContextMode(ctx, false)
}

func acquireHostMembershipContextMode(ctx context.Context, localBudget bool) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	if scope, _ := ctx.Value(hostMembershipContextKey{}).(*hostMembershipContext); scope != nil {
		release, err := scope.enter()
		if err != nil {
			return ctx, nil, err
		}
		bound, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(scope.ctx, cancel)
		return bound, sync.OnceFunc(func() { stop(); cancel(); release() }), nil
	}
	unlock, err := lockHostsFile(ctx)
	if err != nil {
		return ctx, nil, err
	}
	budget, cancel := ctx, func() {}
	if localBudget {
		budget, cancel = context.WithTimeout(ctx, hostsWait)
	}
	bound, end := heldHostMembershipContext(budget)
	return bound, sync.OnceFunc(func() { end(); unlock(); cancel() }), nil
}

func withHostMembershipContext(ctx context.Context, run func(context.Context) error) error {
	bound, release, err := acquireHostMembershipContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	return run(bound)
}

func requireHostMembershipContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scope, _ := ctx.Value(hostMembershipContextKey{}).(*hostMembershipContext)
	if scope == nil {
		return hostStateRemovalError("the canonical host membership lease is not held")
	}
	return scope.check()
}
