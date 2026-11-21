//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"reflect"
)

func (p *bridgeRewindPreparationV1) checkRewindRetryRoomV1(ctx context.Context, old *ColdResumeV1) error {
	lease := p.accounting
	if lease == nil || lease.operation != p.operation || lease.cold != old || p.publication == nil {
		return rewindFailureV1("the retry lost its original complete memory cap")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return p.publication.gateControl(ctx, func(control *placementControl) error {
		if active, exists := control.Active[p.publication.operation.token]; !exists || active != p.publication.operation.record {
			return rewindFailureV1("the retry publication changed before its resource check")
		}
		if lease.takenOver {
			_, err := memoryRoomLockedV1(ctx, p.operation, lease.value.MemoryMax)
			return err
		}
		facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
		if err != nil {
			return err
		}
		if facts.Room < 0 {
			facts.Needed = lease.value.MemoryMax
			return noMemoryRoomV1(facts)
		}
		return nil
	})
}

func (p *bridgeRewindPreparationV1) transferFailedRewindAccountingV1(ctx context.Context, receipt rewindRetryCustodyV1, old, next *ColdResumeV1, attempt *startupLaunchV1) error {
	if p == nil || ctx == nil || p.operation == nil {
		return rewindFailureV1("the failed creation has no original accounting transfer")
	}
	operation := p.operation
	lease := p.accounting
	if receipt == nil || p.retry != receipt || receipt.previousColdV1() != old || next == nil || next.operation != operation || lease == nil || lease.cold != old || lease.operation != operation || lease.origin == nil || lease.origin.rewind != p || p.publication == nil {
		return rewindFailureV1("the actual failed creation lost its original admission cap")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := receipt.checkV1(ctx, operation); err != nil {
		return err
	}
	return p.publication.gateControl(ctx, func(control *placementControl) error {
		if active, exists := control.Active[p.publication.operation.token]; !exists || active != p.publication.operation.record {
			return rewindFailureV1("the original retry publication changed its native active record")
		}
		if err := errors.Join(p.publication.owner.checkIdentity(), operation.native.check(ctx)); err != nil {
			return err
		}
		current, err := operation.LoadContext(ctx)
		if err != nil {
			return err
		}
		if !bytes.Equal(current.VMNeedV1, lease.need) {
			return rewindFailureV1("the original retry changed its admitted VM Need")
		}
		selected, err := startupRuntimeBytes(current)
		if err != nil || !bytes.Equal(selected, lease.runtime) {
			return errors.Join(rewindFailureV1("the original retry changed its admitted runtime"), err)
		}
		if !lease.takenOver {
			if lease.cancelled || !reflect.DeepEqual(current.ResumePreparation, &lease.value) {
				return rewindFailureV1("the original pending retry cap changed")
			}
			facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
			if err != nil {
				return err
			}
			if facts.Room < 0 {
				facts.Needed = lease.value.MemoryMax
				return noMemoryRoomV1(facts)
			}
		} else if current.ResumePreparation != nil {
			return rewindFailureV1("the failed native takeover left another pending reservation")
		}
		operation.gate.Lock()
		defer operation.gate.Unlock()
		if operation.nativeColdResume != old || operation.startup != attempt || operation.resumePreparation != lease || !operation.active.Load() {
			return rewindFailureV1("the failed retry no longer owns the exact original operation fields")
		}
		operation.nativeColdResume = nil
		operation.startup = nil
		if operation.restartMemory != nil && operation.restartMemory.used {
			operation.restartMemory = nil
		}
		if lease.takenOver {
			lease.cancelled = true
			operation.resumePreparation = nil
			p.accounting = nil
		} else {
			// Transfer the actual still-held cap to a new typed attempt. The
			// old one-use transfer remains consumed; no accounting state is
			// reset from a saved row or a failed-start flag.
			nextLease := &resumePreparationLeaseV1{origin: lease.origin, operation: operation, cold: next, publication: lease.publication, value: lease.value, need: bytes.Clone(lease.need), runtime: bytes.Clone(lease.runtime), published: lease.published}
			operation.resumePreparation = nextLease
			p.accounting = nextLease
			lease.cancelled = true
		}
		return nil
	})
}
