//go:build linux

package host

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"golang.org/x/sys/unix"
)

// CaptureSandboxPrepareParentCleanupV1 captures only the actual selected
// native roots of this SAME fresh private prepare loan, before any factory.
func (holder *OwnedSupervisorLease) CaptureSandboxPrepareParentCleanupV1(operation *SessionOperation) (result *SandboxPrepareParentCleanupV1, failure error) {
	if holder == nil || operation == nil {
		return nil, sessionLeaseError("prepare parent capture lost its original native owners")
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	boot := holder.startup
	if boot == nil || boot.owner != holder || boot.operation != operation || operation.originalBoot != boot ||
		!operation.IsSandboxPrepareBootV1() || holder.pending == nil || holder.pending.owner != holder || holder.pending.line != boot.line || holder.pending.peer == nil ||
		holder.pending.peer.identity == nil || boot.line == nil || boot.line.nativePeer != holder.pending.peer ||
		boot.loan.PrepareParent == nil || !boot.loan.PrepareParent.Known() || !boot.loan.PrepareParent.Directory {
		return nil, sessionLeaseError("prepare parent capture has no genuine original private loan")
	}
	if holder.prepareCleanup != nil {
		if holder.prepareCleanup.operation != operation || holder.prepareCleanup.original != holder.prepareCleanup {
			return nil, sessionLeaseError("prepare parent capture belongs to another original operation")
		}
		return holder.prepareCleanup, nil
	}
	if err := errors.Join(operation.native.check(operation.lifetime), holder.native.check(operation.lifetime), holder.pending.peer.Check()); err != nil {
		return nil, err
	}
	parent, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if err != nil {
		return nil, err
	}
	r := &sessionReservation{id: operation.Primary(), parent: parent, launched: true}
	cleanup := &SandboxPrepareParentCleanupV1{holder: holder, operation: operation, peer: holder.pending.peer, parentRef: *boot.loan.PrepareParent}
	cleanup.original = cleanup
	cleanup.base = &sandboxPrepareCleanupV1{operation: operation, reservation: r, child: cleanup}
	complete := false
	defer func() {
		if !complete {
			closeErr := cleanup.Close()
			failure = errors.Join(failure, closeErr)
			if closeErr != nil {
				// Preserve the SAME actual partial native anchors on failure.
				// They are not an issued removal capability.
				result = cleanup
			}
		}
	}()
	if err := cleanup.parentRef.CheckRoot(parent); err != nil {
		return nil, err
	}
	if err := checkSandboxPrepareNamedParentV1(cleanup.parentRef); err != nil {
		return nil, err
	}
	r.dir, err = operation.native.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	dir, err := r.dir.Open(".")
	if err != nil {
		return nil, err
	}
	r.created, err = dir.Stat()
	err = errors.Join(err, dir.Close())
	if err != nil {
		return nil, err
	}
	named, err := parent.Lstat(r.id)
	if err != nil || !os.SameFile(named, r.created) {
		return nil, errors.Join(sessionLeaseError("prepare parent capture found another selected native child"), err)
	}
	if err := errors.Join(operation.native.rootRef.CheckRoot(r.dir), holder.native.rootRef.CheckRoot(r.dir),
		cleanup.parentRef.CheckRoot(parent), checkSandboxPrepareNamedParentV1(cleanup.parentRef)); err != nil {
		return nil, err
	}
	cleanup.opInfo, err = operation.native.file.Stat()
	if err != nil {
		return nil, err
	}
	cleanup.holderInfo, err = holder.native.file.Stat()
	if err != nil {
		return nil, err
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if operation.sandboxPrepareCleanup != nil || !operation.active.Load() {
		return nil, sessionLeaseError("the original private prepare already owns another directory cleanup")
	}
	operation.sandboxPrepareCleanup = cleanup.base
	holder.prepareCleanup = cleanup
	complete = true
	return cleanup, nil
}

func (cleanup *SandboxPrepareParentCleanupV1) checkNativeV1() error {
	if cleanup == nil || cleanup.original != cleanup || cleanup.closed || cleanup.base == nil || cleanup.operation == nil || cleanup.holder == nil ||
		cleanup.holder.prepareCleanup != cleanup || cleanup.operation.sandboxPrepareCleanup != cleanup.base ||
		!cleanup.operation.IsSandboxPrepareBootV1() || cleanup.holder.startup != cleanup.operation.originalBoot ||
		cleanup.holder.pending == nil || cleanup.holder.pending.peer != cleanup.peer || cleanup.peer == nil {
		return sessionLeaseError("the original private prepare parent cleanup source ended or was copied")
	}
	r := cleanup.base.reservation
	if r == nil || r.parent == nil || r.dir == nil || r.created == nil {
		return sessionLeaseError("prepare parent cleanup lost its actual native reservation")
	}
	if err := errors.Join(cleanup.parentRef.CheckRoot(r.parent), checkSandboxPrepareNamedParentV1(cleanup.parentRef),
		cleanup.operation.native.rootRef.CheckRoot(r.dir), cleanup.holder.native.rootRef.CheckRoot(r.dir)); err != nil {
		return err
	}
	name := r.id
	if cleanup.base.claim != "" {
		name = cleanup.base.claim
	}
	named, err := r.parent.Lstat(name)
	if err != nil || !os.SameFile(named, r.created) {
		return errors.Join(sessionLeaseError("the original prepare parent or selected child was replaced"), err)
	}
	for _, binding := range []struct {
		name      string
		file      *os.File
		info      os.FileInfo
		reference cachefs.Reference
		claimed   bool
		claim     string
	}{{lockFile, cleanup.operation.native.file, cleanup.opInfo, cleanup.operation.native.fileRef, cleanup.base.terminal, cleanup.base.terminalClaim},
		{holderLockFile, cleanup.holder.native.file, cleanup.holderInfo, cleanup.holder.native.fileRef, cleanup.base.holderTerminal, cleanup.base.holderClaim}} {
		current, err := binding.file.Stat()
		if err != nil || binding.info == nil || !os.SameFile(binding.info, current) {
			return errors.Join(sessionLeaseError("prepare parent cleanup lost an original native descriptor"), err)
		}
		_, _, uid, links, known := footprintIdentity(current)
		wantLinks := uint64(1)
		if binding.claimed && binding.claim == "" {
			wantLinks = 0 // Only after this SAME owner completed its own unlink.
		}
		if !known || uid != uint64(os.Getuid()) || links != wantLinks || !current.Mode().IsRegular() || current.Mode().Perm() != 0o600 || current.Size() != 0 {
			return sessionLeaseError("an original terminal prepare descriptor changed its native role")
		}
		if !binding.claimed {
			if err := binding.reference.CheckFile(r.dir, binding.name, binding.file, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cleanup *SandboxPrepareParentCleanupV1) parentExitedV1() (bool, error) {
	if err := cleanup.checkNativeV1(); err != nil {
		return false, err
	}
	cleanup.peer.mu.Lock()
	defer cleanup.peer.mu.Unlock()
	if cleanup.peer.closed || cleanup.peer.pidfd < 0 || cleanup.peer.identity == nil {
		return false, sessionLeaseError("prepare cleanup lost its original outside-parent native lifetime")
	}
	for {
		polls := []unix.PollFd{{Fd: int32(cleanup.peer.pidfd), Events: unix.POLLIN}}
		n, err := unix.Poll(polls, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, nil
		}
		if n != 1 || polls[0].Revents&unix.POLLIN == 0 || polls[0].Revents&(unix.POLLERR|unix.POLLNVAL|unix.POLLOUT) != 0 {
			return false, sessionLeaseError("the original outside parent has no positive native exit")
		}
		return true, nil
	}
}

// RemoveAfterParentExitV1 is called only after the SAME supervisor's actual
// runtime destruction, disk, agents, publication and other cleanup have joined.
// Its own native checks independently forbid removal while the parent lives.
func (cleanup *SandboxPrepareParentCleanupV1) RemoveAfterParentExitV1() (bool, error) {
	if cleanup == nil {
		return false, sessionLeaseError("prepare cleanup has no original native parent owner")
	}
	cleanup.mu.Lock()
	defer cleanup.mu.Unlock()
	exited, err := cleanup.parentExitedV1()
	if err != nil || !exited {
		return false, err
	}
	if err := cleanupSandboxPrepareSessionV1(cleanup.operation, cleanup.base.reservation); err != nil {
		return false, err
	}
	cleanup.closed = true
	return true, nil
}
