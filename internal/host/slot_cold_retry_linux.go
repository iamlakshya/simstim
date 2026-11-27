//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

// One actual original slot retry retains the genuine failed Cold creator,
// its native unit and the continuous original allocation lease. Its data or
// previous pointer cannot create this class.
type slotRetryOwnerV2 struct {
	issuer         *slotRetryOwnerV2
	origin         *SlotSnapshotOwnerV1
	receipt        *rewindFailedCreationV1
	previous, next *ColdResumeV1
	oldArtifacts   *auxiliaryArtifactsV1
	operation      *slotlaunch.CheckpointOperationOwnerV1
	published      bool
}

func (owner *SlotSnapshotOwnerV1) retryColdForRewindV1(ctx context.Context, p *bridgeRewindPreparationV1) error {
	done, err := owner.beginV1(ctx)
	if err != nil {
		return err
	}
	defer done()
	if p == nil || p.slotSnapshot != owner || p.operation != owner.operation || p.cold == nil || owner.cold != p.cold || p.phase.Load() != rewindRestartingV1 || p.retryCount >= 3 {
		return rewindFailureV1("the slot retry has no SAME actual failed rewind creation")
	}
	old := p.cold
	operation := p.operation
	if err := p.checkCommitEnvelopeV1(ctx, p.fresh); err != nil {
		return err
	}
	operation.gate.Lock()
	attempt := operation.startup
	operation.gate.Unlock()
	if attempt != nil {
		attempt.mu.Lock()
		ready := attempt.ready
		attempt.mu.Unlock()
		if ready {
			return rewindFailureV1("a committed actual slot creation cannot be treated as failed")
		}
		if err := attempt.Close(); err != nil {
			return err
		}
	}
	old.mu.Lock()
	handed := old.handed
	old.mu.Unlock()
	var settlementErr error
	if handed {
		settlementErr = old.SettleForCancellationV1(ctx, operation)
	}
	old.life.Lock()
	defer old.life.Unlock()
	owner.operations.Lock()
	defer owner.operations.Unlock()
	old.mu.Lock()
	defer old.mu.Unlock()
	if old.closed || old.operation != operation || operation.nativeColdResume != old || old.vacancy == nil || old.artifacts == nil || old.artifacts.slot == nil || old.artifacts.slot.owner != owner || old.handed || old.vacancy.holder == nil {
		return errors.Join(rewindFailureV1("the failed slot creator has not transferred an actual holder vacancy"), settlementErr)
	}
	before := attempt == nil || !attempt.started && attempt.unit == nil && attempt.command == nil
	if before && old.vacancy.stopped != owner.warm.owner {
		return rewindFailureV1("the unstarted retry lost its actual original whole-stop source")
	}
	if !before && (old.creation == nil || old.rewindCreationGroup == nil || attempt == nil || !attempt.cleanupConfirmed || !attempt.exited) {
		return errors.Join(rewindFailureV1("the actual failed slot creator has no positive native settlement"), settlementErr)
	}
	retry := owner.slotRetry
	if retry == nil || retry.previous != old {
		receipt := &rewindFailedCreationV1{preparation: p, operation: operation, previous: old, launch: attempt, creation: old.creation, group: old.rewindCreationGroup, vacancy: old.vacancy, beforeDispatch: before, boot: BootID(), previousReceipt: p.retry}
		retry = &slotRetryOwnerV2{origin: owner, receipt: receipt, previous: old, oldArtifacts: old.artifacts}
		retry.issuer = retry
		owner.slotRetry = retry
	}
	if retry.issuer != retry || retry.origin != owner || retry.receipt.previousColdV1() != old {
		return rewindFailureV1("the slot retry changed its actual previous Cold owner")
	}
	previousReceipt := p.retry
	p.retry = retry.receipt
	committed := false
	defer func() {
		if !committed {
			p.retry = previousReceipt
		}
	}()
	if err := retry.receipt.checkV1(ctx, operation); err != nil {
		return err
	}
	if err := p.checkRewindRetryRoomV1(ctx, old); err != nil {
		return err
	}
	slot := old.artifacts.slot
	if err := slot.checkOldClosureV2(); err != nil {
		return err
	}
	if !before {
		if err := owner.abandonFailedRootV2(ctx, retry); err != nil {
			return err
		}
	}
	// Legitimate writes by the actual failed new creator may change mtime.
	// The native disk inode and admitted length, not a journal, stay bound.
	actual, err := old.artifacts.disk.Stat()
	if err != nil {
		return err
	}
	current, err := owner.roles[9].Stat()
	if err != nil || !os.SameFile(actual, current) || actual.Size() != owner.need.DiskMax {
		return errors.Join(rewindFailureV1("the failed actual slot changed its original disk identity or size"), err)
	}
	if err := old.artifacts.checkRetainedBootDescriptorsV1(ctx); err != nil {
		return err
	}
	if err := old.vacancy.holder.check(ctx); err != nil {
		return err
	}
	originalStopped, originalFailed := old.vacancy.stopped, old.vacancy.rewindFailed
	if !before {
		old.vacancy.stopped = nil
		old.vacancy.rewindFailed = retry.receipt
	}
	defer func() {
		if !committed {
			old.vacancy.stopped, old.vacancy.rewindFailed = originalStopped, originalFailed
		}
	}()
	next := retry.next
	if next == nil {
		next = &ColdResumeV1{operation: operation, vacancy: old.vacancy}
		retry.next = next
	}
	if next.artifacts != nil {
		if err := next.artifacts.Close(); err != nil {
			return err
		}
		next.artifacts = nil
	}
	owner.cold = next
	nextArtifacts, err := owner.captureColdArtifactsV2(ctx, next)
	if nextArtifacts != nil {
		next.artifacts = nextArtifacts
	}
	if err != nil {
		owner.cold = old
		return err
	}
	if err := p.transferFailedRewindAccountingV1(ctx, retry.receipt, old, next, attempt); err != nil {
		owner.cold = old
		return err
	}
	slot.retireTo = nextArtifacts.slot
	old.vacancy, old.artifacts, old.creation, old.rewindCreationGroup = nil, nil, nil, nil
	old.closed = true
	p.cold = next
	p.artifactsRefreshed = true
	p.retryCount++
	retry.published = true
	committed = true
	if err := retry.oldArtifacts.Close(); err != nil {
		owner.failedArtifacts = append(owner.failedArtifacts, retry.oldArtifacts)
		return err
	}
	if p.journal != nil {
		return p.journal.phaseV1(ctx, "ready_to_restart")
	}
	return nil
}

func (owner *SlotSnapshotOwnerV1) abandonFailedRootV2(ctx context.Context, retry *slotRetryOwnerV2) error {
	if retry == nil || retry.issuer != retry || retry.origin != owner || retry.receipt == nil || retry.receipt.creation == nil || retry.receipt.group == nil || retry.oldArtifacts == nil || retry.oldArtifacts.slot == nil {
		return rewindFailureV1("slot abandonment has no actual failed creation and old reservation")
	}
	if err := retry.receipt.checkV1(ctx, owner.operation); err != nil {
		return err
	}
	if retry.operation != nil {
		if err := retry.operation.Close(); err != nil {
			return err
		}
		retry.operation = nil
	}
	roles := append(append([]*os.File(nil), retry.oldArtifacts.slot.roles...), retry.receipt.creation, retry.receipt.group)
	worker, err := slotlaunch.RunReservedCheckpointOperationV2(ctx, roles, slotlaunch.CheckpointOperationV1{Session: owner.operation.Primary(), Action: "abandon"})
	retry.operation = worker // retain before inspecting late error/ACK/close
	if worker != nil {
		closeErr := worker.Close()
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			retry.operation = nil
		}
	}
	return errors.Join(err, retry.receipt.checkV1(ctx, owner.operation))
}

// A later real creation consumes the failed vacancy. Its prior native birth
// and whole-CG closure remain facts even while that NEW unit is live; manager
// absence is required only before the new original dispatch, not after it.
func (owner *SlotSnapshotOwnerV1) checkColdRetrySourceV2(ctx context.Context, cold *ColdResumeV1) error {
	if ctx == nil || cold == nil || cold.vacancy == nil || owner == nil || owner.issuer != owner || owner.warm == nil || owner.warm.owner == nil || owner.operation == nil || owner.cold != cold {
		return rewindFailureV1("the reserved Cold lost its actual native vacancy")
	}
	if cold.vacancy.stopped == owner.warm.owner {
		return nil
	}
	retry := owner.slotRetry
	if retry == nil || retry.issuer != retry || retry.origin != owner || retry.next != cold || retry.receipt == nil || cold.vacancy.rewindFailed != retry.receipt {
		return rewindFailureV1("the reserved Cold has no actual original failed creation")
	}
	receipt := retry.receipt
	if !cold.handed {
		return receipt.checkV1(ctx, owner.operation)
	}
	if !cold.bound || cold.startup == nil || cold.creation == nil || cold.startup.operation != owner.operation || owner.operation.nativeColdResume != cold || receipt.preparation == nil || receipt.preparation.retry != receipt || receipt.preparation.operation != owner.operation || owner.operation.rewindPreparation != receipt.preparation || receipt.boot != BootID() {
		return rewindFailureV1("the new reserved creator lost its original failed-source custody")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.operation.native.check(ctx)); err != nil {
		return err
	}
	if receipt.beforeDispatch {
		if receipt.launch != nil && (receipt.launch.started || receipt.launch.unit != nil || receipt.launch.command != nil) {
			return rewindFailureV1("a real dispatched creator cannot use pre-dispatch retry custody")
		}
		return nil
	}
	if receipt.creation == nil || receipt.group == nil {
		return rewindFailureV1("the previous actual failed creator has no retained birth and unit")
	}
	if _, err := slotSnapshotPIDV1(receipt.creation, false); err != nil {
		return err
	}
	empty, err := originalStopCgroupEmptyV1(receipt.group)
	if err != nil || !empty {
		return errors.Join(rewindFailureV1("the previous actual failed unit has no whole native vacancy"), err)
	}
	return nil
}
