package host

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type stateMetadataPublisher struct {
	ctx       context.Context
	cancel    context.CancelCauseFunc
	operation *placementOperation
	stop      chan struct{}
	watched   chan struct{}
	once      sync.Once
	release   error
}

func beginStateMetadataPublisher(ctx context.Context) (*stateMetadataPublisher, error) {
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
	publisher := &stateMetadataPublisher{ctx: owned, cancel: cancel, operation: operation, stop: make(chan struct{}), watched: make(chan struct{})}
	go publisher.watch()
	return publisher, nil
}

func checkStateMetadataPublisher(ctx context.Context, control *placementControl, operation *placementOperation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CheckCaller(); err != nil {
		return err
	}
	if operation == nil || operation.owner == nil {
		return placementError("the state producer has no active publisher")
	}
	if len(control.extra) != 0 {
		return placementError("future control fields grant no state publication authority")
	}
	if current, exists := control.Active[operation.token]; !exists || current != operation.record {
		return placementError("the state producer lost its exact active receipt")
	}
	if control.Drain != "" {
		return UserError("draining", "host removal cancelled the state producer", "Finish the current removal before retrying the host operation.")
	}
	if control.ShutdownCommitted != nil {
		return UserError("forbidden", "the host committed idle shutdown before metadata publication", "Wait for the next boot before refreshing host metadata.")
	}
	return operation.owner.checkIdentity()
}

func (p *stateMetadataPublisher) watch() {
	defer close(p.watched)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.stop:
			return
		case <-ticker.C:
			err := p.operation.owner.withControl(p.ctx, func(control *placementControl) error {
				return checkStateMetadataPublisher(p.ctx, control, p.operation)
			}, false)
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

func (p *stateMetadataPublisher) close() error {
	p.once.Do(func() {
		p.cancel(context.Canceled)
		close(p.stop)
		<-p.watched
		p.release = p.operation.Close()
	})
	return p.release
}
