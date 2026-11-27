//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"syscall"

	"golang.org/x/sys/unix"
)

// This is a retained result of the actual final quiesced inspection. It
// carries work-loss policy only; physical stop and native creator ownership
// remain separate mandatory inputs at the storage consumer.
type purgeOrdinaryWorkLossV2 struct {
	purge       *purgeOwnerV1
	operation   *SessionOperation
	preparation *OrdinaryPurgePreparationV2
	permit      *ordinaryActiveGitInspectionPermitV2
	inspection  *joinedOrdinaryGitInspectionV2
}

func sealOrdinaryWorkLossV2(ctx context.Context, purge *purgeOwnerV1, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2, permit *ordinaryActiveGitInspectionPermitV2, inspection *joinedOrdinaryGitInspectionV2) (*purgeOrdinaryWorkLossV2, error) {
	if purge == nil || purge.args.Force || operation == nil || preparation == nil || permit == nil || inspection == nil || permit.purge != purge || inspection.permit != permit {
		return nil, sessionLeaseError("the final work-loss guard has no actual same original purge inspection")
	}
	if err := preparation.CheckQuiesced(ctx, operation); err != nil {
		return nil, err
	}
	counts, err := inspection.Counts(ctx, operation, preparation)
	if err != nil {
		return nil, err
	}
	if counts.Dirty != 0 || counts.Unpushed != 0 {
		return nil, UserError("unsaved", "the original worktree has changed files or unpushed commits", "Save the listed work before removal, or use --force after reviewing what will be lost.")
	}
	if err := preparation.CheckQuiesced(ctx, operation); err != nil {
		return nil, err
	}
	return &purgeOrdinaryWorkLossV2{purge: purge, operation: operation, preparation: preparation, permit: permit, inspection: inspection}, nil
}

func (proof *purgeOrdinaryWorkLossV2) checkStoppedV2(ctx context.Context, purge *purgeOwnerV1, operation *SessionOperation, stopped *StoppedOrdinarySessionV1) error {
	if proof == nil || ctx == nil || purge == nil || proof.purge != purge || operation == nil || proof.operation != operation || proof.preparation == nil || proof.preparation.owner == nil || stopped == nil || stopped.ordinaryHolderCaptureV2 != proof.preparation.owner || proof.permit == nil || proof.inspection == nil || proof.inspection.permit != proof.permit {
		return sessionLeaseError("the stopped cleanup lost its actual original final work-loss proof")
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	if err := proof.inspection.checkOriginalJoinedReadV2(); err != nil {
		return err
	}
	if proof.inspection.counts.Dirty != 0 || proof.inspection.counts.Unpushed != 0 {
		return sessionLeaseError("the actual original final inspection was not clean")
	}
	return proof.permit.checkStoppedTransitionV2(ctx, purge, operation, proof.preparation, stopped)
}

func (owner *joinedOrdinaryGitInspectionV2) checkOriginalJoinedReadV2() error {
	if owner == nil {
		return sessionLeaseError("the original joined inspection is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || !owner.complete || owner.command == nil || owner.command.Process == nil || owner.peer == nil || owner.peer.pid != owner.command.Process.Pid || owner.waitDone == nil || owner.waitErr != nil {
		return sessionLeaseError("the actual original final inspection did not retain its native helper join")
	}
	select {
	case <-owner.waitDone:
	default:
		return sessionLeaseError("the actual final helper Wait is not joined")
	}
	state := owner.command.ProcessState
	if state == nil || state.Pid() != owner.peer.pid {
		return sessionLeaseError("the actual final helper has no same-process wait result")
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Exited() || status.ExitStatus() != 0 {
		return sessionLeaseError("the original final helper did not exit successfully")
	}
	return owner.checkJoinedHelperProcessV2()
}

// The same original atomic handle and actual Cmd.Wait are mandatory even on
// cancellation or a failed read. A logical read completion cannot close loans.
func (owner *joinedOrdinaryGitInspectionV2) checkJoinedHelperProcessV2() error {
	if owner == nil || owner.command == nil || owner.command.Process == nil || owner.creation == nil || owner.creationFD < 0 || int(owner.creation.Fd()) != owner.creationFD || owner.waitDone == nil {
		return sessionLeaseError("the fixed helper has no retained original creation join source")
	}
	select {
	case <-owner.waitDone:
	default:
		return sessionLeaseError("the same original fixed-helper Wait has not joined")
	}
	state := owner.command.ProcessState
	if state == nil || state.Pid() != owner.command.Process.Pid {
		return sessionLeaseError("the original fixed-helper Wait has no same-process native result")
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Exited() && !status.Signaled() {
		return sessionLeaseError("the original fixed-helper native wait status is unknown")
	}
	if owner.waitErr != nil {
		var exited *exec.ExitError
		if !errors.As(owner.waitErr, &exited) || exited.ProcessState != state {
			return errors.Join(sessionLeaseError("the original fixed-helper Wait returned unknown closure"), owner.waitErr)
		}
	}
	if err := unix.PidfdSendSignal(int(owner.creation.Fd()), 0, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	polls := []unix.PollFd{{Fd: int32(owner.creation.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(sessionLeaseError("the same original fixed-helper creation has no positive kernel exit"), err)
	}
	return nil
}

func (permit *ordinaryActiveGitInspectionPermitV2) checkStoppedTransitionV2(ctx context.Context, purge *purgeOwnerV1, operation *SessionOperation, preparation *OrdinaryPurgePreparationV2, stopped *StoppedOrdinarySessionV1) error {
	if permit == nil {
		return sessionLeaseError("the actual original inspection permit is absent")
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.closed || permit.purge != purge || permit.operation != operation || permit.preparation != preparation || preparation == nil || preparation.owner == nil || stopped == nil || stopped.ordinaryHolderCaptureV2 != preparation.owner || stopped.origin != purge.placement {
		return sessionLeaseError("the actual inspection roots did not follow their same original physical stop")
	}
	if err := stopped.checkStoppedOrdinaryNativeFactsV1(ctx); err != nil {
		return err
	}
	if err := purge.gate(func(control *placementControl, _ *os.Root) error {
		current, found := control.WorktreeOrigins[operation.Primary()]
		if !found || !reflect.DeepEqual(current, permit.origin) {
			return sessionLeaseError("the original worktree creator receipt changed during physical cleanup")
		}
		return nil
	}); err != nil {
		return err
	}
	// Cleanup may already have renamed/unlinked this original tree. Keep its
	// actual descriptors; never renew them from the saved capture name.
	for _, pair := range []struct {
		root *os.Root
		ref  purgeRef
	}{{permit.work, permit.origin.Directory}, {permit.parent, permit.origin.Parent}, {permit.repository, permit.origin.RepoRef}, {permit.common, permit.origin.CommonRef}} {
		if pair.root == nil {
			return sessionLeaseError("the original work-loss proof lost a real retained root")
		}
		if err := pair.ref.matchesRoot(pair.root); err != nil {
			return err
		}
	}
	return errors.Join(ctx.Err(), CheckCaller())
}
