package host

import (
	"context"
	"os"
	"time"
)

type placementOperation struct {
	owner  *placement
	token  string
	record placementOperationRecord
}

func beginPlacementOperation(ctx context.Context, kind string) (*placementOperation, error) {
	if kind != "start" && kind != "publish" {
		return nil, placementError("the placement operation kind is invalid")
	}
	p, err := placementForCall()
	if err != nil {
		return nil, err
	}
	if kind == "start" && p.installer {
		return nil, placementError("an installer payload cannot start or resume a session")
	}
	token, err := footprintNonce()
	if err != nil {
		return nil, err
	}
	birth, err := placementProcessBirth(os.Getpid())
	if err != nil {
		return nil, err
	}
	record := placementOperationRecord{PID: os.Getpid(), Birth: birth, Kind: kind, At: time.Now().UTC()}
	err = withPlacement(ctx, false, func(control *placementControl) error { control.Active[token] = record; return nil }, true)
	if err != nil {
		return nil, err
	}
	return &placementOperation{owner: p, token: token, record: record}, nil
}

func (op *placementOperation) Close() error {
	if op == nil || op.owner == nil {
		return nil
	}
	return op.owner.withControl(context.Background(), func(control *placementControl) error {
		current, exists := control.Active[op.token]
		if !exists {
			return nil
		}
		if current != op.record {
			return placementError("the active placement receipt changed identity")
		}
		delete(control.Active, op.token)
		return nil
	}, true)
}

func waitPlacementOperations(ctx context.Context, p *placement) error {
	for {
		busy := false
		err := p.withControl(ctx, func(control *placementControl) error {
			changed := false
			for token, op := range control.Active {
				birth, err := placementProcessBirth(op.PID)
				if os.IsNotExist(err) || err == nil && birth != op.Birth {
					delete(control.Active, token)
					changed = true
					continue
				}
				if err != nil {
					return placementError("cannot establish the lifetime of an active placement operation")
				}
				busy = true
			}
			if !changed {
				return errPlacementNoChange
			}
			return nil
		}, true)
		if err != nil {
			return err
		}
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
