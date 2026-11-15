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
	bootOwner  *originalBootV1
	startup    *SessionOperation
}

// beginRuntimePublication admits this current helper before cache roots/parts
// exist. Its reservation outlives every owned descriptor and partial cleanup.
func beginRuntimePublication(ctx context.Context) (*runtimePublication, error) {
	return beginRuntimePublicationKind(ctx, "publish", nil, nil)
}

func beginStartupPublication(ctx context.Context, operation *SessionOperation) (*runtimePublication, error) {
	if operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, sessionLeaseError("the startup placement has no original owning operation")
	}
	return beginRuntimePublicationKind(ctx, "start", nil, operation)
}

func beginBootAssetPublication(ctx context.Context, boot *originalBootV1) (*runtimePublication, error) {
	if err := checkBootPublisher(boot); err != nil {
		return nil, err
	}
	return beginRuntimePublicationKind(ctx, "publish", boot, nil)
}

func beginAutomaticAgentsPublication(ctx context.Context, boot *originalBootV1) (*runtimePublication, error) {
	if boot == nil || boot.operation == nil || boot.owner == nil || boot.operation.originalBoot != boot || boot.loan.Purpose != "automatic" || boot.operation.AutomaticBoot() == nil || !boot.owner.Valid(boot.loan.Primary) {
		return nil, automaticRestoreError()
	}
	return beginRuntimePublicationKind(ctx, "publish", boot, nil)
}

func beginRuntimePublicationKind(ctx context.Context, kind string, boot *originalBootV1, startup *SessionOperation) (*runtimePublication, error) {
	if boot == nil {
		if err := CheckCaller(); err != nil {
			return nil, err
		}
	} else if err := checkBootPublisher(boot); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, placementError("the original publisher has no owner context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, err := beginPlacementOperation(ctx, kind)
	if err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancelCause(ctx)
	publication := &runtimePublication{ctx: owned, cancel: cancel, owner: operation.owner, operation: operation, stop: make(chan struct{}), watched: make(chan struct{}), bootOwner: boot, startup: startup}
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
				if err := p.checkCaller(); err != nil {
					return err
				}
				return p.owner.withControl(p.ctx, func(control *placementControl) error {
					if control.Drain != "" {
						return UserError("draining", "purge cancelled this runtime or startup operation", "Finish the current removal before trying another runtime install.")
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
	if err := p.checkCaller(); err != nil {
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

func checkBootPublisher(boot *originalBootV1) error {
	if boot == nil || boot.owner == nil || boot.operation == nil || boot.operation.originalBoot != boot ||
		!boot.owner.Valid(boot.loan.Primary) || !boot.operation.Valid(boot.loan.Primary) {
		return vmNeedError("the asset publisher lost its actual native boot owner")
	}
	return nil
}
func (p *runtimePublication) checkCaller() error {
	if p != nil && p.bootOwner != nil {
		return checkBootPublisher(p.bootOwner)
	}
	if p != nil && p.startup != nil && (!p.startup.Valid(p.startup.Primary()) || p.startup.ownerContext == nil || p.startup.ownerContext.Err() != nil) {
		return sessionLeaseError("the original startup placement owner ended")
	}
	return CheckCaller()
}
