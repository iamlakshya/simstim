package host

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// This private consumer is born from the actual original reservation and OP,
// after the SAME startup's native cleanup ACK and exit (or before creation).
// It retains its atomically claimed original directory across cleanup retries.
type sandboxPrepareCleanupV1 struct {
	child          *SandboxPrepareParentCleanupV1
	mu             sync.Mutex
	operation      *SessionOperation
	reservation    *sessionReservation
	claim          string
	mount          uint64
	terminal       bool
	terminalClaim  string
	holderTerminal bool
	holderClaim    string
	removed        bool
}

func cleanupSandboxPrepareSessionV1(operation *SessionOperation, reservation *sessionReservation) error {
	if operation == nil || reservation == nil || reservation.parent == nil || reservation.dir == nil || reservation.created == nil ||
		!validID(reservation.id) || operation.Primary() != reservation.id {
		return sessionLeaseError("prepare cleanup lost its original reservation and operation")
	}
	operation.gate.Lock()
	owner := operation.sandboxPrepareCleanup
	if owner == nil {
		owner = &sandboxPrepareCleanupV1{operation: operation, reservation: reservation}
		operation.sandboxPrepareCleanup = owner
	}
	operation.gate.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.operation != operation || owner.reservation != reservation {
		return sessionLeaseError("prepare cleanup belongs to another native reservation")
	}
	if owner.removed {
		return operation.CloseWithError()
	}
	if owner.claim == "" {
		if err := owner.capture(); err != nil {
			return err
		}
	}
	if err := owner.check(); err != nil {
		return err
	}
	entries, err := stoppedCleanupEntriesV1Portable(reservation.dir)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if entry.Name() == lockFile || entry.Name() == owner.terminalClaim || entry.Name() == owner.holderClaim ||
			(owner.child != nil && entry.Name() == holderLockFile) {
			continue
		}
		if err := owner.removeEntry(reservation.dir, entry.Name(), 0, &count); err != nil {
			return err
		}
	}
	if owner.child != nil {
		if err := owner.removeTerminalV1(holderLockFile, owner.child.holder.native.file, owner.child.holder.native.fileRef, &owner.holderTerminal, &owner.holderClaim); err != nil {
			return err
		}
	}
	if err := owner.removeTerminalV1(lockFile, operation.native.file, operation.native.fileRef, &owner.terminal, &owner.terminalClaim); err != nil {
		return err
	}
	if err := owner.check(); err != nil {
		return err
	}
	if err := reservation.parent.Remove(owner.claim); err != nil {
		return err
	}
	owner.removed = true
	// No path-based reservation.close follows removal. Its old public name
	// may already belong to a different reservation, which remains untouched.
	closed := errors.Join(reservation.dir.Close(), reservation.parent.Close())
	return errors.Join(closed, operation.CloseWithError())
}

func (owner *sandboxPrepareCleanupV1) capture() error {
	o, r := owner.operation, owner.reservation
	if owner.child != nil {
		if exited, err := owner.child.parentExitedV1(); err != nil || !exited {
			return errors.Join(sessionLeaseError("the original outside prepare parent has no positive native exit"), err)
		}
	} else if !o.Valid(r.id) || o.native.borrowed {
		return sessionLeaseError("prepare cleanup has no current original owning OP")
	}
	if !sandboxPrepareLaunchCleanupConfirmedV1(o) {
		return sessionLeaseError("prepare cleanup has no native destruction acknowledgement and exit")
	}
	if err := o.native.rootRef.CheckRoot(r.dir); err != nil {
		return err
	}
	current, err := r.parent.Lstat(r.id)
	if err != nil || !os.SameFile(r.created, current) {
		return errors.Join(sessionLeaseError("the original reserved prepare directory changed"), err)
	}
	// The concrete driver destroys its own resources. Residual external names
	// grant no removal rights, even when their text matches the session ID.
	for _, path := range []string{paths.Socket(r.id), paths.HookSocket(r.id), paths.SandboxRoot(r.id)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(sessionLeaseError("prepare cleanup preserved an unconfirmed external runtime path"), err)
		}
	}
	directory, err := r.dir.Open(".")
	if err != nil {
		return err
	}
	mount, mountErr := sandboxPrepareMountIdentityV1(directory)
	closeErr := directory.Close()
	if mountErr != nil || closeErr != nil {
		return errors.Join(mountErr, closeErr)
	}
	nonce, err := api.RandomSessionID()
	if err != nil {
		return err
	}
	claim := ".prepare-cleanup-" + r.id + "-" + nonce
	if err := cachefs.RenameNoReplace(r.parent, r.id, claim); err != nil {
		return err
	}
	claimed, err := r.parent.Lstat(claim)
	if err != nil || !os.SameFile(r.created, claimed) {
		return errors.Join(sessionLeaseError("prepare cleanup preserved a replaced atomic directory claim"), err, cachefs.RenameNoReplace(r.parent, claim, r.id))
	}
	owner.claim, owner.mount = claim, mount
	return owner.check()
}

func (owner *sandboxPrepareCleanupV1) check() error {
	o, r := owner.operation, owner.reservation
	if owner.child != nil {
		if exited, err := owner.child.parentExitedV1(); err != nil || !exited {
			return errors.Join(sessionLeaseError("the original prepare parent cleanup is unconfirmed"), err)
		}
	} else if o == nil || o.native == nil || !o.active.Load() || o.native.borrowed || owner.claim == "" {
		return sessionLeaseError("the original prepare cleanup owner ended")
	}
	if err := o.native.rootRef.CheckRoot(r.dir); err != nil {
		return err
	}
	claimed, err := r.parent.Lstat(owner.claim)
	if err != nil || !os.SameFile(r.created, claimed) {
		return errors.Join(sessionLeaseError("the retained original prepare directory claim changed"), err)
	}
	if !owner.terminal {
		return o.native.fileRef.CheckFile(r.dir, lockFile, o.native.file, 0o600)
	}
	return nil
}

func (owner *sandboxPrepareCleanupV1) removeEntry(root *os.Root, name string, depth int, count *int) error {
	*count += 1
	if depth > 128 || *count > 100000 {
		return sessionLeaseError("the original prepare cleanup tree exceeds its inspection bound")
	}
	if err := owner.check(); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	ref := refOf(info)
	if ref.UID != uint64(os.Getuid()) || info.Mode().Perm()&0o022 != 0 ||
		info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return sessionLeaseError("prepare cleanup preserved a linked, foreign or unknown resource")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	native, statErr := file.Stat()
	mount, mountErr := sandboxPrepareMountIdentityV1(file)
	closeErr := file.Close()
	if statErr != nil || mountErr != nil || closeErr != nil || !ref.matches(native) || mount != owner.mount {
		return errors.Join(sessionLeaseError("prepare cleanup preserved a changed or mounted resource"), statErr, mountErr, closeErr)
	}
	nonce, err := api.RandomSessionID()
	if err != nil {
		return err
	}
	claim := ".prepare-owned-" + nonce
	if err := cachefs.RenameNoReplace(root, name, claim); err != nil {
		return err
	}
	claimed, err := root.Lstat(claim)
	if err != nil || !ref.matches(claimed) {
		return errors.Join(sessionLeaseError("prepare cleanup preserved a replaced resource claim"), err, cachefs.RenameNoReplace(root, claim, name))
	}
	if info.IsDir() {
		child, err := root.OpenRoot(claim)
		if err != nil {
			return err
		}
		defer child.Close()
		entries, err := stoppedCleanupEntriesV1Portable(child)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := owner.removeEntry(child, entry.Name(), depth+1, count); err != nil {
				return err
			}
		}
	}
	if err := owner.check(); err != nil {
		return err
	}
	claimed, err = root.Lstat(claim)
	if err != nil || !ref.matches(claimed) {
		return errors.Join(sessionLeaseError("the captured prepare resource changed before removal"), err)
	}
	return root.Remove(claim)
}

func stoppedCleanupEntriesV1Portable(root *os.Root) ([]os.DirEntry, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := file.ReadDir(-1)
	return entries, errors.Join(err, file.Close())
}

// Keep the cleanup context distinct from a cancelled construction request.
// It is the SAME operation's original lifetime, never a new execution source.
func sandboxPrepareCleanupContextV1(operation *SessionOperation) context.Context {
	return operation.lifetime
}

func (b *bridge) joinSandboxPrepareDirectoryCleanupV1(operation *SessionOperation, reservation *sessionReservation) {
	for {
		if err := cleanupSandboxPrepareSessionV1(operation, reservation); err == nil {
			return
		} else if b.log != nil {
			b.log.Warn("fixed test retains its original directory cleanup owner", "session", reservation.id, "err", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
