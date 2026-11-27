package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/publicationlease"
)

type publicationSourceContextV1 struct{}

// This constructor is the only publication-fence issuer. It retains the
// existing operation, control root and native domain. Readiness is durable
// before any caller can acquire a helper loan; a creating record cannot lend.
func beginPlacementPublicationV1(ctx context.Context, kind string) (*placementOperation, error) {
	p, err := placementForCall()
	if err != nil {
		return nil, err
	}
	return beginPlacementPublicationOwnedV1(ctx, p, kind)
}

// Original boot callers pass their captured admission, never a fresh lookup.
func beginPlacementPublicationOwnedV1(ctx context.Context, p *placement, kind string) (*placementOperation, error) {
	if p == nil || kind != "start" && kind != "publish" && kind != "auxiliary" && kind != "resume_prepare" && kind != "rewind_prepare" {
		return nil, placementError("the placement publication has no original owner or valid kind")
	}
	return p.admitPlacementConstructorV1(ctx, func() (*placementOperation, error) {
		op, err := beginPlacementOperationOwnedV1(ctx, p, kind)
		if err != nil {
			return op, err
		}
		err = op.owner.withControlRoot(ctx, func(control *placementControl, root *os.Root) error {
			if control.Active[op.token] != op.record {
				return placementError("the publication lost its original creating operation")
			}
			var createErr error
			op.fence, createErr = publicationlease.CreateReservedV1(ctx, root, op.owner.domain(), op.token, func(record string) error {
				op.record.Fence, op.record.FenceCreating = record, true
				control.Active[op.token] = op.record
				return writePlacementControl(root, *control)
			})
			if createErr != nil {
				op.fenceFailed = true
				return createErr
			}
			op.record.FenceCreating = false
			control.Active[op.token] = op.record
			return writePlacementControl(root, *control)
		}, false)
		return op, err
	})
}

func validatePublicationOperationV1(control placementControl, token string, op placementOperationRecord) error {
	if op.Fence == "" {
		if op.FenceCreating || op.FenceRemoving {
			return placementError("an unfenced operation has a publication cleanup phase")
		}
		return nil
	}
	record, err := publicationlease.DecodeRecordV1(op.Fence)
	if err != nil {
		return err
	}
	if record.Token != token || record.Domain != (cachefs.Domain{Scope: control.Scope, HostID: control.HostID}) || op.FenceCreating && op.FenceRemoving {
		return placementError("the publication fence has another original operation or domain")
	}
	return nil
}

func samePublicationOperationV1(actual, original placementOperationRecord) bool {
	// These are the only in-flight postimages of this SAME private constructor.
	// A failed metadata sync can leave either the previous or proposed phase.
	if actual.Fence != "" && actual.Fence != original.Fence {
		return false
	}
	actual.Fence, original.Fence = "", ""
	actual.FenceCreating, original.FenceCreating = false, false
	actual.FenceRemoving, original.FenceRemoving = false, false
	return actual == original
}

func (op *placementOperation) closePublicationFenceV1(ctx context.Context) error {
	if op.fenceClosed {
		return nil
	}
	if op.fenceFailed {
		// The failed native factory cannot lend any helper. Its unnamed or
		// original created inode is removed using the retained creator handles.
		if err := op.fence.CloseUnpublishedV1(); err != nil {
			return err
		}
	} else {
		if err := op.fence.FreezeParentV1(); err != nil {
			return err
		}
		if _, err := op.fence.ProbeV1(ctx); err != nil {
			return err
		}
	}
	err := op.owner.withControlRoot(ctx, func(control *placementControl, root *os.Root) error {
		current, found := control.Active[op.token]
		if !found {
			return nil
		}
		if !samePublicationOperationV1(current, op.record) {
			return placementError("the original fenced operation changed identity")
		}
		if !op.fenceFailed {
			proof, err := op.fence.ProbeV1(ctx)
			if err != nil {
				return err
			}
			if !current.FenceRemoving {
				if err := proof.CheckV1(); err != nil {
					return err
				}
				current.Fence, current.FenceCreating, current.FenceRemoving = op.fence.RecordV1(), false, true
				control.Active[op.token], op.record = current, current
				// Persist the exact deletion intent while its fresh EX is held.
				// A failed final write can then resume the actual unlink/sync.
				if err := writePlacementControl(root, *control); err != nil {
					return err
				}
			}
			if err := proof.RemoveV1(); err != nil {
				return err
			}
		}
		delete(control.Active, op.token)
		return nil
	}, true)
	if err != nil {
		return err
	}
	if err := op.fence.CloseV1(); err != nil {
		return err
	}
	op.fenceClosed = true
	return nil
}

// Parent identity can reject reaping, but cannot establish helper closure.
// This runs only after the original parent is known ended. Fresh EX decides
// whether its actual inherited OFD has any surviving helper copies.
func reapPublicationFenceV1(ctx context.Context, p *placement, control *placementControl, root *os.Root, token string, op placementOperationRecord) (bool, error) {
	var proof *publicationlease.ExclusiveV1
	var err error
	if op.FenceCreating || op.FenceRemoving {
		proof, err = publicationlease.ResumeRemovalV1(ctx, root, p.domain(), op.Fence)
	} else {
		proof, err = publicationlease.TryExclusiveV1(ctx, root, p.domain(), op.Fence)
	}
	if errors.Is(err, publicationlease.ErrBusy) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer proof.CloseV1()
	if !op.FenceRemoving {
		op.FenceCreating, op.FenceRemoving = false, true
		control.Active[token] = op
		if err := writePlacementControl(root, *control); err != nil {
			return false, err
		}
	}
	if err := proof.RemoveV1(); err != nil {
		return false, err
	}
	return false, nil
}

func retainPublicationHelperV1(ctx context.Context) (*os.File, error) {
	op, ok := ctx.Value(publicationSourceContextV1{}).(*placementOperation)
	if !ok || op == nil {
		return nil, placementError("the converter has no original publication source")
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.fence == nil || op.fenceFailed || op.fenceClosed || op.record.FenceCreating || op.record.FenceRemoving {
		return nil, placementError("the original publication no longer permits helper creation")
	}
	if err := op.owner.withControl(ctx, func(control *placementControl) error {
		if current, found := control.Active[op.token]; !found || current != op.record || control.Drain != "" {
			return placementError("the converter lost its exact current publication operation")
		}
		return nil
	}, false); err != nil {
		return nil, err
	}
	return op.fence.RetainForHelperV1(ctx)
}
