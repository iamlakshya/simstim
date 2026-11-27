package host

import (
	"context"
	"errors"
	"sync"
)

// This belongs to the existing native placement, not to its durable ledger.
// Only actual failed constructors can populate pending while holding gate.
// One unresolved original cleanup prevents every later reservation, bounding
// this retained ownership to one object without a timer or retry goroutine.
type placementPublicationCleanupV1 struct {
	once    sync.Once
	gate    chan struct{}
	pending *placementOperation
}

func (owner *placementPublicationCleanupV1) lockV1(ctx context.Context) (func(), error) {
	owner.once.Do(func() { owner.gate = make(chan struct{}, 1); owner.gate <- struct{}{} })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-owner.gate:
		return func() { owner.gate <- struct{}{} }, nil
	}
}

func (p *placement) retryPlacementConstructorLockedV1(ctx context.Context) error {
	owner := &p.publicationCleanup
	if owner.pending != nil {
		if err := owner.pending.closeContextV1(ctx); err != nil {
			return err
		}
		owner.pending = nil
	}
	return ctx.Err()
}

// No caller-owned callback is exposed: the two concrete private constructors
// below hold this gate only through reservation/native source metadata work.
func (p *placement) admitPlacementConstructorV1(ctx context.Context, create func() (*placementOperation, error)) (*placementOperation, error) {
	unlock, err := p.publicationCleanup.lockV1(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := p.retryPlacementConstructorLockedV1(ctx); err != nil {
		return nil, err
	}
	op, err := create()
	if err == nil {
		return op, nil
	}
	if op == nil {
		return nil, err
	}
	// create returns the actual original object even on an ambiguous control
	// write. Preserve it before returning an error if its cleanup cannot join.
	cleanup := op.closeContextV1(ctx)
	if cleanup != nil {
		p.publicationCleanup.pending = op
	}
	return nil, errors.Join(err, cleanup)
}

func (p *placement) retryPlacementConstructorV1(ctx context.Context) error {
	unlock, err := p.publicationCleanup.lockV1(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return p.retryPlacementConstructorLockedV1(ctx)
}
