package host

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/publicationlease"
)

type placementOperation struct {
	mu          sync.Mutex
	owner       *placement
	token       string
	record      placementOperationRecord
	fence       *publicationlease.SourceV1
	fenceFailed bool
	fenceClosed bool
}

func beginPlacementOperation(ctx context.Context, kind string) (*placementOperation, error) {
	if kind != "start" && kind != "publish" && kind != "auxiliary" && kind != "resume_prepare" && kind != "rewind_prepare" {
		return nil, placementError("the placement operation kind is invalid")
	}
	p, err := placementForCall()
	if err != nil {
		return nil, err
	}
	return p.admitPlacementConstructorV1(ctx, func() (*placementOperation, error) {
		return beginPlacementOperationOwnedV1(ctx, p, kind)
	})
}

// The caller already holds this SAME native placement's constructor gate.
// No public role, record or callback data can enter this inner constructor.
func beginPlacementOperationOwnedV1(ctx context.Context, p *placement, kind string) (*placementOperation, error) {
	if (kind == "start" || kind == "auxiliary" || kind == "resume_prepare" || kind == "rewind_prepare") && p.installer {
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
	op := &placementOperation{owner: p, token: token, record: record}
	reserve := func(control *placementControl) error {
		if control.Scope != p.scope || control.HostID != p.hostID {
			return placementError("the constructor changed its original native placement")
		}
		control.Active[token] = record
		return nil
	}
	if kind == "auxiliary" {
		err = withAuxiliaryPlacementV1(ctx, reserve, true)
	} else {
		err = withPlacement(ctx, false, reserve, true)
	}
	if err != nil {
		// The write may have committed before a later native sync failure.
		// Return the actual object to the owning constructor gate, not JSON.
		return op, err
	}
	return op, nil
}

func (op *placementOperation) Close() error {
	return op.closeContextV1(context.Background())
}
func (op *placementOperation) closeContextV1(ctx context.Context) error {
	if op == nil || op.owner == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if op.fence != nil {
		return op.closePublicationFenceV1(ctx)
	}
	return op.owner.withControl(ctx, func(control *placementControl) error {
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
		if err := p.retryPlacementConstructorV1(ctx); err != nil {
			return err
		}
		busy := false
		err := p.withControlRoot(ctx, func(control *placementControl, root *os.Root) error {
			changed := false
			for token, op := range control.Active {
				birth, err := placementProcessBirth(op.PID)
				if os.IsNotExist(err) || err == nil && birth != op.Birth {
					if op.Fence != "" {
						pending, err := reapPublicationFenceV1(ctx, p, control, root, token, op)
						if err != nil {
							return err
						}
						if pending {
							busy = true
							continue
						}
					}
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
