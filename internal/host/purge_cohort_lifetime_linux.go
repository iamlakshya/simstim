//go:build linux

package host

import (
	"context"
	"errors"
	"os"
)

// ProposeAbortV3 proposes the global abort on the SAME native pair. A commit
// which already won remains a commit; this method never changes that packet.
func (owner *PurgeCohortDecisionV3) ProposeAbortV3(ctx context.Context) (byte, error) {
	if owner == nil || ctx == nil {
		return 0, placementError("the original native cohort abort is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := errors.Join(ctx.Err(), owner.checkFilesV3()); err != nil {
		return 0, err
	}
	value, err := proposePurgeCohortDecisionV3(owner.receiver, owner.sender, purgeCohortAbortV3)
	return byte(value), err
}

// FirstDecisionV3 observes only the actual first queued native packet. Zero
// means no decision yet. Unknown state is an error, never an abort/commit.
func (owner *PurgeCohortDecisionV3) FirstDecisionV3(ctx context.Context) (byte, error) {
	if owner == nil || ctx == nil {
		return 0, placementError("the original native cohort decision is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := errors.Join(ctx.Err(), owner.checkFilesV3()); err != nil {
		return 0, err
	}
	value, err := peekPurgeCohortDecisionV3(owner.receiver)
	return byte(value), err
}

// Close releases this actual pair only after every original monitor loan has
// been physically joined and returned by its real source owner. Closing a
// socket or losing a coordinator is never a subject-local decision.
func (owner *PurgeCohortDecisionV3) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return observeOriginalPurgeDescriptorCloseV3([]*os.File{owner.receiver, owner.sender}, nil)
	}
	if err := owner.checkFilesV3(); err != nil {
		return err
	}
	if len(owner.loans) != 0 {
		return placementError("the original native cohort still has actual monitor decision loans")
	}
	if _, err := proposePurgeCohortDecisionV3(owner.receiver, owner.sender, purgeCohortAbortV3); err != nil {
		return err
	}
	failure := owner.closeUnusedV3()
	observed := observeOriginalPurgeDescriptorCloseV3([]*os.File{owner.receiver, owner.sender}, nil)
	if observed == nil {
		owner.closed = true
	}
	return errors.Join(failure, observed)
}
