//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/api"
	"os"
	"reflect"
	"slices"

	"golang.org/x/sys/unix"
)

// This receipt consumes an actual failed creation, never a stopped row. It
// owns the original atomic creation pidfd and live-captured whole cgroup.
type rewindFailedCreationV1 struct {
	preparation     *bridgeRewindPreparationV1
	operation       *SessionOperation
	previous        *ColdResumeV1
	launch          *startupLaunchV1
	creation, group *os.File
	vacancy         *auxiliaryVacancyV1
	beforeDispatch  bool
	boot            string
	previousReceipt rewindRetryCustodyV1
	closed          bool
	closeErr        error
}

type rewindSlotRetryV1 interface {
	retryColdForRewindV1(context.Context, *bridgeRewindPreparationV1) error
}

func (receipt *rewindFailedCreationV1) previousColdV1() *ColdResumeV1 {
	if receipt == nil {
		return nil
	}
	return receipt.previous
}

// A genuine failed creation changes the protected run generation. Only its
// retained native retry receipt may accept that new structural generation;
// the original client metadata and complete member identities stay fixed.
func (p *bridgeRewindPreparationV1) checkColdRestartCohortV1(ctx context.Context, declared *api.SandboxSnapshot) error {
	if p == nil || !reflect.DeepEqual(declared, p.source) {
		return snapshotChanged()
	}
	if p.retry == nil {
		return CheckSandboxSnapshotLocked(p.selection.ID, p.operation.Primary(), declared)
	}
	if err := p.retry.checkV1(ctx, p.operation); err != nil {
		return err
	}
	var root *Manifest
	children := make([]*Manifest, 0, len(p.members)-1)
	for _, member := range p.members {
		current, err := member.read(ctx, p.operation)
		if err != nil || current == nil {
			return errors.Join(snapshotChanged(), err)
		}
		if member.manifest == nil || !current.CreatedAt.Equal(member.manifest.CreatedAt) || current.Agent.Kind != member.manifest.Agent.Kind {
			return snapshotChanged()
		}
		if member.id == p.operation.Primary() {
			root = current
		} else {
			children = append(children, current)
		}
	}
	if root == nil {
		return snapshotChanged()
	}
	actual, err := snapshotFromProtected(root, children, p.selection.ID)
	if err != nil {
		return err
	}
	if actual.Primary != declared.Primary || actual.Version != declared.Version || !slices.Equal(actual.Members, declared.Members) {
		return snapshotChanged()
	}
	return nil
}

func (p *bridgeRewindPreparationV1) checkReadyCreationOutcomeV1(ctx context.Context, attempt *startupLaunchV1) error {
	if p == nil || p.cold == nil || p.operation == nil || attempt == nil || p.operation.startup != attempt || p.cold.startup != attempt || attempt.operation != p.operation || !attempt.ready {
		return rewindFailureV1("the ready outcome lost its actual original creation")
	}
	if err := p.cold.Check(ctx, p.operation); err == nil {
		return nil
	}
	p.cold.mu.Lock()
	creation, group := p.cold.creation, p.cold.rewindCreationGroup
	p.cold.mu.Unlock()
	if creation == nil || group == nil {
		return rewindFailureV1("the ended ready creation has no retained actual birth and cgroup")
	}
	polls := []unix.PollFd{{Fd: int32(creation.Fd()), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err == nil && count == 0 {
		return p.checkLiveReadyCreationV1(ctx, attempt, creation)
	}
	if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(rewindFailureV1("the ready creation has neither a current native owner nor positive exit"), err)
	}
	empty, err := originalStopCgroupEmptyV1(group)
	if err != nil || !empty {
		return errors.Join(rewindFailureV1("the ended ready creation's actual cgroup is not empty"), err)
	}
	return errors.Join(ctx.Err(), p.operation.native.check(ctx))
}

// Ready cleanup may have closed the launch's own peer. A live late-result
// retry therefore captures a genuine current Warm under the SAME outside OP,
// and binds it to the still-held original creation rather than reopening a
// saved PID or interpreting the ready row as a new source.
func (p *bridgeRewindPreparationV1) checkLiveReadyCreationV1(ctx context.Context, attempt *startupLaunchV1, creation *os.File) error {
	if p == nil || p.cold == nil || p.operation == nil || attempt == nil || attempt.unit == nil || creation == nil || p.cold.creation != creation || p.cold.startup != attempt || p.operation.startup != attempt || !attempt.ready {
		return rewindFailureV1("the live ready outcome lost its original creation")
	}
	var err error
	if p.readyWarm == nil && p.readyUnexpected == nil {
		p.readyWarm, p.readyUnexpected, err = InspectResumeForOperationV1(ctx, p.operation)
		if err != nil {
			return err
		}
	}
	if p.readyWarm == nil || p.readyUnexpected != nil {
		return rewindFailureV1("the original live ready creation has no genuine current Warm owner")
	}
	if err := p.readyWarm.Check(ctx, p.operation); err != nil {
		return err
	}
	owner := p.readyWarm.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	invocation := owner.invocation
	if invocation == nil || invocation.peer == nil || owner.operation != p.operation || invocation.path != attempt.unit.path || invocation.unit != attempt.unit.unit || !sameUnitActivationProperties(invocation.properties, attempt.unit.properties) || !os.SameFile(invocation.executable, attempt.unit.executable) || !validOriginalReadyNeed(attempt.manifest, &owner.source) {
		return rewindFailureV1("the current ready owner belongs to another native invocation or admitted boot")
	}
	first, err := slotSnapshotPIDV1(creation, true)
	if err != nil {
		return err
	}
	// The current peer's immutable PID is only a comparison between two
	// genuinely live pidfd owners. It is never used to open or adopt a peer.
	if err := invocation.peer.checkLive(); err != nil {
		return err
	}
	if invocation.peer.pid != first {
		return errors.Join(rewindFailureV1("the actual ready birth differs from its current native owner"), err)
	}
	last, err := slotSnapshotPIDV1(creation, true)
	if err != nil || last != first {
		return errors.Join(rewindFailureV1("the original ready creation ended during observation"), err)
	}
	return errors.Join(ctx.Err(), p.operation.native.check(ctx), invocation.Check(ctx))
}

func (p *bridgeRewindPreparationV1) retryFailedSlotCreationV1(ctx context.Context) error {
	actual, ok := p.slotSnapshot.(rewindSlotRetryV1)
	if !ok {
		return rewindFailureV1("the actual reserved slot factory has no failed-creation custody transfer")
	}
	return actual.retryColdForRewindV1(ctx, p)
}

func captureRewindCreationGroupV1(ctx context.Context, operation *SessionOperation, launch *startupLaunchV1) (returned error) {
	if operation == nil || operation.rewindPreparation == nil {
		return nil
	}
	p := operation.rewindPreparation
	cold := operation.nativeColdResume
	if ctx == nil || cold == nil || p.cold != cold || !p.active.Load() || !p.consumed || p.phase.Load() != rewindRestartingV1 || operation.startup != launch || launch == nil || launch.unit == nil {
		return rewindFailureV1("the restart cgroup has no actual original rewind creation")
	}
	if err := errors.Join(launch.unit.checkCreationV1(ctx), launch.checkNativeMemoryV1(ctx, launch.manifest)); err != nil {
		return err
	}
	group, err := openNativeStopCgroupV1(operation.Primary())
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			returned = errors.Join(returned, group.Close())
		}
	}()
	if err := errors.Join(launch.unit.checkCreationV1(ctx), launch.checkNativeMemoryV1(ctx, launch.manifest)); err != nil {
		return err
	}
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if cold.closed || !cold.bound || !cold.handed || cold.creation == nil || cold.startup != launch || cold.operation != operation || operation.startup != launch {
		return rewindFailureV1("the live-created restart cgroup changed before its native custody was retained")
	}
	if cold.rewindCreationGroup != nil {
		before, err := cold.rewindCreationGroup.Stat()
		if err != nil {
			return err
		}
		now, err := group.Stat()
		if err != nil || !os.SameFile(before, now) {
			return errors.Join(rewindFailureV1("the actual restart creation changed cgroup identity"), err)
		}
		return nil
	}
	cold.rewindCreationGroup = group
	keep = true
	return nil
}

func (receipt *rewindFailedCreationV1) checkV1(ctx context.Context, operation *SessionOperation) error {
	if receipt == nil || ctx == nil || receipt.closed || operation == nil || operation != receipt.operation || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || receipt.preparation == nil || receipt.preparation.operation != operation || operation.rewindPreparation != receipt.preparation || receipt.preparation.retry != receipt || !receipt.preparation.active.Load() || !receipt.preparation.consumed || receipt.boot != BootID() || receipt.vacancy == nil || receipt.vacancy.manager == nil || receipt.vacancy.operation != operation {
		return rewindFailureV1("the failed creation retry lost its actual original custody")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), operation.native.check(ctx), receipt.vacancy.manager.checkNativeManager()); err != nil {
		return err
	}
	if receipt.beforeDispatch {
		if receipt.launch != nil && (receipt.launch.started || receipt.launch.unit != nil || receipt.launch.command != nil) {
			return rewindFailureV1("a dispatched attempt cannot use the original unconsumed stopped owner")
		}
		if receipt.vacancy.stopped == nil {
			return rewindFailureV1("the pre-dispatch retry lost its original stopped owner")
		}
		return receipt.vacancy.stopped.Check(ctx, operation)
	}
	if receipt.launch == nil || !receipt.launch.started || receipt.creation == nil || receipt.group == nil {
		return rewindFailureV1("the failed restart has no retained actual creation and whole cgroup")
	}
	polls := []unix.PollFd{{Fd: int32(receipt.creation.Fd()), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(rewindFailureV1("the actual failed restart creation has no positive exit"), err)
	}
	empty, err := originalStopCgroupEmptyV1(receipt.group)
	if err != nil || !empty {
		return errors.Join(rewindFailureV1("the actual failed restart cgroup is not proved empty"), err)
	}
	if err := receipt.vacancy.manager.requireAuxiliaryUnitAbsentV1(ctx, receipt.vacancy.primary); err != nil {
		return err
	}
	if err := receipt.vacancy.manager.requireAuxiliaryUnitAbsentV1(ctx, receipt.vacancy.auxiliary); err != nil {
		return err
	}
	return ctx.Err()
}

func (receipt *rewindFailedCreationV1) Close() error {
	if receipt == nil {
		return nil
	}
	if receipt.closeErr != nil {
		return receipt.closeErr
	}
	if receipt.closed {
		return nil
	}
	if receipt.creation != nil {
		receipt.closeErr = errors.Join(receipt.closeErr, receipt.creation.Close())
	}
	if receipt.group != nil {
		receipt.closeErr = errors.Join(receipt.closeErr, receipt.group.Close())
	}
	if receipt.previousReceipt != nil {
		receipt.closeErr = errors.Join(receipt.closeErr, receipt.previousReceipt.Close())
	}
	receipt.closed = receipt.closeErr == nil
	return receipt.closeErr
}

func (p *bridgeRewindPreparationV1) retryFailedRewindCreationV1(ctx context.Context) error {
	if p == nil || p.operation == nil || p.cold == nil || p.phase.Load() != rewindRestartingV1 {
		return rewindFailureV1("retry has no actual original failed restart")
	}
	if err := p.checkCommitEnvelopeV1(ctx, p.fresh); err != nil {
		return err
	}
	if p.retryCount >= 3 {
		return rewindFailureV1("the original rewind exhausted its bounded native restart attempts; its actual owners are retained")
	}
	if p.slotSnapshot != nil {
		return p.retryFailedSlotCreationV1(ctx)
	}
	old := p.cold
	operation := p.operation
	operation.gate.Lock()
	attempt := operation.startup
	operation.gate.Unlock()
	if attempt != nil {
		attempt.mu.Lock()
		ready := attempt.ready
		attempt.mu.Unlock()
		if ready {
			return rewindFailureV1("a ready native runtime cannot be cancelled or reused as a failed creation")
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
	old.mu.Lock()
	defer old.mu.Unlock()
	if old.closed || old.operation != operation || operation.nativeColdResume != old || old.vacancy == nil || old.artifacts == nil || old.handed || old.vacancy.holder == nil {
		return errors.Join(rewindFailureV1("the actual failed restart did not transfer a genuine holder vacancy"), settlementErr)
	}
	beforeDispatch := attempt == nil || !attempt.started
	if beforeDispatch && old.vacancy.stopped == nil {
		return rewindFailureV1("the unstarted attempt lost the original whole-stop source")
	}
	if !beforeDispatch && (old.creation == nil || old.rewindCreationGroup == nil || attempt == nil || !attempt.cleanupConfirmed || !attempt.exited) {
		return errors.Join(rewindFailureV1("the failed native creation is not physically settled"), settlementErr)
	}
	receipt := &rewindFailedCreationV1{preparation: p, operation: operation, previous: old, launch: attempt, creation: old.creation, group: old.rewindCreationGroup, vacancy: old.vacancy, beforeDispatch: beforeDispatch, boot: BootID(), previousReceipt: p.retry}
	previousReceipt := p.retry
	originalStopped, originalFailed := old.vacancy.stopped, old.vacancy.rewindFailed
	p.retry = receipt
	complete := false
	defer func() {
		if !complete {
			p.retry = previousReceipt
			old.vacancy.stopped, old.vacancy.rewindFailed = originalStopped, originalFailed
		}
	}()
	if err := receipt.checkV1(ctx, operation); err != nil {
		return err
	}
	if err := p.checkRewindRetryRoomV1(ctx, old); err != nil {
		return err
	}
	// The original raw disk may have legitimate writes from this failed
	// actual creation. Check the SAME inode/size and all immutable boot roles.
	if old.artifacts.diskParent == nil {
		return rewindFailureV1("the failed local creation lost its original disk parent")
	}
	if err := old.artifacts.diskRef.CheckCreatedFile(old.artifacts.diskParent, "disk.img", old.artifacts.disk, 0600); err != nil {
		return err
	}
	disk, err := old.artifacts.disk.Stat()
	if err != nil || disk.Size() != old.artifacts.need.DiskMax {
		return errors.Join(rewindFailureV1("the failed creation changed its actual disk identity or size"), err)
	}
	if err := old.artifacts.checkRetainedBootDescriptorsV1(ctx); err != nil {
		return err
	}
	if err := old.vacancy.holder.check(ctx); err != nil {
		return err
	}
	if !beforeDispatch {
		old.vacancy.stopped = nil
		old.vacancy.rewindFailed = receipt
	}
	nextArtifacts, err := captureAuxiliaryArtifactsV1(ctx, old.vacancy)
	if err != nil {
		if !beforeDispatch {
			old.vacancy.stopped = p.stopped
			old.vacancy.rewindFailed = nil
		}
		return err
	}
	next := &ColdResumeV1{operation: operation, vacancy: old.vacancy, artifacts: nextArtifacts}
	if err := p.transferFailedRewindAccountingV1(ctx, receipt, old, next, attempt); err != nil {
		return errors.Join(err, nextArtifacts.Close())
	}
	oldArtifacts := old.artifacts
	old.vacancy = nil
	old.artifacts = nil
	old.creation = nil
	old.rewindCreationGroup = nil
	old.closed = true
	p.cold = next
	p.artifactsRefreshed = true
	p.retryCount++
	complete = true
	if err := oldArtifacts.Close(); err != nil {
		old.closeErr = err
		return err
	}
	if p.journal != nil {
		if err := p.journal.phaseV1(ctx, "ready_to_restart"); err != nil {
			return err
		}
	}
	return nil
}
