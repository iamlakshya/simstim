package host

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type runtimePublication struct {
	ctx        context.Context
	cancel     context.CancelCauseFunc
	owner      *placement
	operation  *placementOperation
	stop       chan struct{}
	watched    chan struct{}
	once       sync.Once
	watchStop  sync.Once
	releaseErr error
	activity   sync.Mutex
}

// beginRuntimePublication admits this current helper before cache roots/parts
// exist. Its reservation outlives every owned descriptor and partial cleanup.
func beginRuntimePublication(ctx context.Context) (*runtimePublication, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, err := beginPlacementOperation(ctx, "publish")
	if err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancelCause(ctx)
	publication := &runtimePublication{ctx: owned, cancel: cancel, owner: operation.owner, operation: operation, stop: make(chan struct{}), watched: make(chan struct{})}
	go publication.watch()
	return publication, nil
}

func (p *runtimePublication) watch() {
	defer close(p.watched)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			err := func() error {
				p.activity.Lock()
				defer p.activity.Unlock()
				if p.ctx.Err() != nil {
					return context.Cause(p.ctx)
				}
				if err := CheckCaller(); err != nil {
					return err
				}
				return p.owner.withControl(p.ctx, func(control *placementControl) error {
					if control.Drain != "" {
						return UserError("draining", "purge cancelled this runtime publisher", "Finish the current removal before trying another runtime install.")
					}
					if current, ok := control.Active[p.operation.token]; !ok || current != p.operation.record {
						return placementError("the runtime publish reservation changed identity")
					}
					return p.owner.checkIdentity()
				}, false)
			}()
			if err != nil {
				var user *proto.Error
				if errors.As(err, &user) && user.Code == "busy" {
					continue
				}
				p.cancel(err)
				return
			}
		}
	}
}

func (p *runtimePublication) gate(ctx context.Context, publish func() error) error {
	if p == nil {
		return placementError("runtime publication has no active publisher receipt")
	}
	p.activity.Lock()
	defer p.activity.Unlock()
	if err := CheckCaller(); err != nil {
		return err
	}
	if p == nil || p.operation == nil {
		return placementError("runtime publication has no active publisher receipt")
	}
	if p.ctx.Err() != nil {
		return context.Cause(p.ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return withPlacement(ctx, false, func(control *placementControl) error {
		if p.ctx.Err() != nil {
			return context.Cause(p.ctx)
		}
		if current, ok := control.Active[p.operation.token]; !ok || current != p.operation.record {
			return placementError("runtime publication lost its exact active reservation")
		}
		return publish()
	}, false)
}

// close runs only after its caller has closed the exact owned parts/store.
func (p *runtimePublication) close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() { p.cancel(context.Canceled); p.stopWatching(); p.releaseErr = p.operation.Close() })
	return p.releaseErr
}

func (p *runtimePublication) stopWatching() { p.watchStop.Do(func() { close(p.stop) }); <-p.watched }
