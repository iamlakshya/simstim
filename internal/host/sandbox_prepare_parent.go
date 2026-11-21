package host

import (
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// SandboxPrepareParentCleanupV1 is issued only by the same genuine private
// prepare loan before construction. Its native roots, holder and original
// outside-parent pidfd supply custody. Serialized comparison data cannot.
type SandboxPrepareParentCleanupV1 struct {
	mu         sync.Mutex
	original   *SandboxPrepareParentCleanupV1
	holder     *OwnedSupervisorLease
	operation  *SessionOperation
	peer       *nativeStartupPeerV1
	parentRef  cachefs.Reference
	base       *sandboxPrepareCleanupV1
	opInfo     os.FileInfo
	holderInfo os.FileInfo
	closed     bool
	closeErr   error
}

func (witness *sandboxPrepareWitnessV1) originalParentComparisonV1(operation *SessionOperation) (cachefs.Reference, error) {
	witness.mu.Lock()
	defer witness.mu.Unlock()
	r := witness.reservation
	if operation == nil || witness.bound != operation || !witness.written || !operation.active.Load() ||
		operation.native == nil || operation.native.borrowed || r == nil || r.parent == nil || r.dir == nil || !witness.parentCheck.Known() {
		return cachefs.Reference{}, sessionLeaseError("prepare startup lost its original reserved native parent")
	}
	if err := witness.parentCheck.CheckRoot(r.parent); err != nil {
		return cachefs.Reference{}, err
	}
	if err := checkSandboxPrepareNamedParentV1(witness.parentCheck); err != nil {
		return cachefs.Reference{}, err
	}
	current, err := r.parent.Lstat(r.id)
	if err != nil || !os.SameFile(r.created, current) {
		return cachefs.Reference{}, errors.Join(sessionLeaseError("the original prepare reservation moved before its native loan"), err)
	}
	if err := operation.native.rootRef.CheckRoot(r.dir); err != nil {
		return cachefs.Reference{}, err
	}
	return witness.parentCheck, nil
}

func checkSandboxPrepareNamedParentV1(reference cachefs.Reference) error {
	current, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if err != nil {
		return err
	}
	return errors.Join(reference.CheckRoot(current), current.Close())
}

// Close releases only this producer's read-only anchors. It cannot remove
// metadata, stop a process or issue a replacement operation.
func (cleanup *SandboxPrepareParentCleanupV1) Close() error {
	if cleanup == nil {
		return nil
	}
	cleanup.mu.Lock()
	defer cleanup.mu.Unlock()
	if cleanup.closeErr != nil {
		return cleanup.closeErr
	}
	if cleanup.closed {
		return nil
	}
	if cleanup.original != cleanup || cleanup.base == nil || cleanup.base.reservation == nil {
		return sessionLeaseError("the original prepare parent cleanup anchor is absent")
	}
	if cleanup.base.removed {
		cleanup.closed = true
		return nil
	}
	r := cleanup.base.reservation
	var err error
	if r.dir != nil {
		err = errors.Join(err, r.dir.Close())
	}
	if r.parent != nil {
		err = errors.Join(err, r.parent.Close())
	}
	cleanup.closeErr = err
	if err == nil {
		cleanup.closed = true
	}
	return err
}
