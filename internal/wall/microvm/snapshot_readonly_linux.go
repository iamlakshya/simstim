//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// Only the actual source-bound receive path attaches this original phase.
// The phase owns the descriptors through physical join; snapshots borrow them.
func (owner *snapshotDiskOwnerV1) adoptReadonlyDiskV2(phase *vmmExecPhaseV1, disk *os.File) error {
	if owner == nil || phase == nil || !phase.readonly || phase.snapshotOwner != owner || disk == nil {
		return fmt.Errorf("the read-only snapshot has no actual received phase/disk")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closing || owner.disk != nil || owner.readonly != nil {
		return fmt.Errorf("the original snapshot owner already captured a disk or ended")
	}
	owner.disk, owner.readonly = disk, phase
	return nil
}

func (owner *snapshotDiskOwnerV1) checkReadonlyRootsV2() error {
	if owner.readonly == nil || owner.diskParent == nil || owner.readonly.snapshotOwner != owner {
		return fmt.Errorf("the original read-only snapshot parent is absent")
	}
	// The session and borrowed lease remain login-owned. The VM directory
	// has undergone the real jail's subuid chown; its anchored FD can be
	// inspected without claiming host-login DAC over its contents.
	if err := owner.sessionRef.CheckRoot(owner.session); err != nil {
		return err
	}
	if _, err := cachefs.ValidateFile(owner.session, owner.leaseName, owner.lease, 0o600); err != nil {
		return err
	}
	current, err := owner.session.Lstat("vm")
	if err != nil {
		return err
	}
	actual, err := owner.diskParent.Stat()
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, actual) {
		return errors.Join(err, fmt.Errorf("the original read-only VM directory changed identity"))
	}
	return nil
}

func (owner *snapshotDiskOwnerV1) fenceBorrowersV2() {
	if owner == nil {
		return
	}
	owner.mu.Lock()
	owner.closing = true
	owner.mu.Unlock()
	owner.borrowers.Wait()
}

// The existing serialized state reply now includes B's actual anchored disk
// witness for this closed purpose. It is checked before and after every loan.
func (phase *vmmExecPhaseV1) checkSnapshotDiskV2(ctx context.Context) error {
	phase.mu.Lock()
	controller, disk, parent := phase.controller, phase.disk, phase.diskParent
	completed := phase.completed
	phase.mu.Unlock()
	if !completed || controller == nil {
		return fmt.Errorf("the original read-only snapshot has no completed native/control phase")
	}
	if err := phase.command.CheckVMMReadonlyDiskV2(phase.source, disk, parent); err != nil {
		return err
	}
	if _, err := controller.call(ctx, "state"); err != nil {
		return err
	}
	return phase.command.CheckVMMReadonlyDiskV2(phase.source, disk, parent)
}

// This captures parent identity only; it grants neither disk readability nor
// native launch. The actual closed RO source checks mapped ownership later.
func (owner *snapshotDiskOwnerV1) checkPinnedParentV2() error {
	if owner == nil || owner.session == nil || owner.diskParent == nil || owner.lease == nil {
		return fmt.Errorf("the actual prepared disk parent is absent")
	}
	if err := owner.sessionRef.CheckRoot(owner.session); err != nil {
		return err
	}
	if _, err := cachefs.ValidateFile(owner.session, owner.leaseName, owner.lease, 0o600); err != nil {
		return err
	}
	current, err := owner.session.Lstat("vm")
	if err != nil {
		return err
	}
	actual, err := owner.diskParent.Stat()
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, actual) || actual.Mode().Perm() != 0o700 {
		return errors.Join(err, fmt.Errorf("the actual prepared VM directory changed identity or private mode"))
	}
	flags, err := unix.FcntlInt(owner.diskParent.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_PATH == 0 {
		return errors.Join(err, fmt.Errorf("the prepared parent is not its actual O_PATH anchor"))
	}
	return nil
}
