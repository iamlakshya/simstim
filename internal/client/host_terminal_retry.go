package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// WithHostStateRetirementContext inspects only an existing post-forget OP.
// It creates no state or diagnostics. found is true only after a complete
// canonical owner receipt proves this original absent-name Target generation.
// work receives the same verified cleanup scope used by ordinary removal.
func WithHostStateRetirementContext(ctx context.Context, name string, inspect HostStateReceiptInspector, work func(context.Context, HostStateReceiptIdentity) error) (found bool, failure error) {
	if !ValidHostName(name) || inspect == nil || work == nil {
		return false, hostStateRemovalError("terminal retry needs its exact name and canonical owner inspector")
	}
	if ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(heldKey{name}) != nil {
		return false, hostStateRemovalError("existing-only retry must begin outside an earlier operation or membership callback")
	}
	budget, cancel := context.WithTimeout(ctx, hostsWait)
	defer cancel()
	owned, err := openExistingHostTerminalOP(budget, name)
	if err != nil || owned == nil {
		return false, err
	}
	if err := takeLock(owned.file); err != nil {
		defer owned.close()
		if errors.Is(err, errLockTaken) {
			return false, busyHostOperation(name, owned)
		}
		return false, wrapHostOperationError(name, hostLockPath(name), err)
	}
	held := &heldLock{native: owned}
	held.terminal.Store(true)
	life := held.stateLifetime()
	end, err := life.retire(budget)
	if err != nil {
		owned.close()
		return false, err
	}
	defer func() {
		held.gone.Store(true)
		life.beginClose()
		end()
		life.drain()
		clearHostForgetWitness(held)
		owned.close()
	}()
	budget, stopOperation := life.retirementContext(budget)
	defer stopOperation()
	budget = context.WithValue(budget, heldKey{name}, held)
	locked, unlock, err := acquireExistingHostMembershipContext(budget)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := owned.check(locked); err != nil {
		return false, err
	}
	current, err := openHostMembershipSnapshot(locked)
	if err != nil {
		return false, err
	}
	defer current.close()
	hosts, err := LoadHosts()
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == name }) {
		return false, hostStateRemovalError("a saved host uses this terminal retry name")
	}
	identity, frame, err := inspectExistingHostTerminal(locked, held, name, inspect)
	if err != nil {
		return false, err
	}
	if identity.Proof.TargetGeneration != current.generation {
		return false, hostStateRemovalError("the terminal receipt belongs to another membership generation")
	}
	identity.Original = cloneObservationHost(identity.Original)
	intent := &hostForgetIntent{original: cloneObservationHost(identity.Original), hostID: identity.HostID, hooks: HostForgetReceiptHooks{Inspect: inspect}}
	intent.hooks.Verify = func(verifying context.Context, raw []byte) (HostForgetMembershipProof, error) {
		got, err := inspect(verifying, bytes.NewReader(raw), int64(len(raw)), name)
		if err != nil {
			return HostForgetMembershipProof{}, err
		}
		if !SameEntry(got.Original, intent.original) || got.HostID != identity.HostID || got.Nonce != identity.Nonce || got.Proof != identity.Proof {
			return HostForgetMembershipProof{}, hostStateRemovalError("the terminal source receipt changed original owner or attempt")
		}
		return got.Proof, nil
	}
	locked = context.WithValue(locked, hostForgetIntentKey{}, intent)
	if frame != nil {
		locked = context.WithValue(locked, hostTerminalRetryKey{}, frame)
	}
	found = true
	return found, runHostForgetCommittedScope(locked, identity.Original, held, current, func(verified context.Context) error {
		reply := identity
		reply.Original = cloneObservationHost(identity.Original)
		return work(verified, reply)
	})
}

func openExistingHostTerminalOP(ctx context.Context, name string) (owned *hostOperationFile, failure error) {
	present, err := existingHostTerminalDomainPresent(ctx, name)
	if err != nil || !present {
		return nil, err
	}
	failure = withExistingHostMembershipContext(ctx, func(locked context.Context) error {
		parent := filepath.Dir(hostLockPath(name))
		root, err := cachefs.OpenPrivateRoot(parent, false)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		keep := false
		defer func() {
			if !keep {
				_ = root.Close()
			}
		}()
		parentRef, err := cachefs.CaptureRoot(root)
		if err != nil {
			return err
		}
		leaf := filepath.Base(hostLockPath(name))
		file, err := cachefs.OpenFile(root, leaf, os.O_RDWR, 0600)
		if errors.Is(err, os.ErrNotExist) {
			for _, existing := range []string{HostFileKey(name), leaf + ".who"} {
				if _, err := root.Lstat(existing); !errors.Is(err, os.ErrNotExist) {
					return hostStateRemovalError("local host state exists without a resumable OP witness")
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		reference, err := cachefs.CaptureFile(root, leaf, file, 0600)
		if err != nil {
			_ = file.Close()
			return err
		}
		owned = &hostOperationFile{root: root, rootPath: parent, stateDir: StateDir(), name: leaf,
			parent: parentRef, file: file, reference: reference}
		keep = true
		return owned.check(locked)
	})
	if failure != nil && owned != nil {
		owned.close()
		owned = nil
	}
	return owned, failure
}

func inspectExistingHostTerminal(ctx context.Context, held *heldLock, name string, inspect HostStateReceiptInspector) (identity HostStateReceiptIdentity, complete *hostTerminalFrame, failure error) {
	reference, err := cachefs.CaptureFile(held.native.root, held.native.name, held.native.file, 0600)
	if err != nil {
		return identity, nil, err
	}
	frame, err := readHostTerminalFrame(ctx, held.native.file, reference.Size)
	if (err == nil || errors.Is(err, errHostTerminalIncomplete)) && len(frame.prefix) != 0 {
		parent, captureErr := cachefs.CaptureRoot(held.native.root)
		if captureErr != nil {
			return identity, nil, captureErr
		}
		if frame.header.OP != terminalFacet(reference) || frame.header.Parent != terminalFacet(parent) {
			return identity, nil, hostStateRemovalError("the checkpoint belongs to another native OP or parent")
		}
	}
	if errors.Is(err, errHostTerminalIncomplete) && reference.Size != 0 && len(frame.prefix) == 0 {
		return identity, nil, hostStateRemovalError("the torn checkpoint has no complete native inventory to reconstruct")
	}
	if err == nil {
		view := &stateFDView{ctx: ctx, r: io.NewSectionReader(held.native.file, frame.receiptStart, frame.header.ReceiptSize)}
		defer view.revoke()
		identity, failure = inspect(ctx, view, frame.header.ReceiptSize, name)
		if failure == nil && !terminalIdentityMatches(frame.header, identity) {
			failure = hostStateRemovalError("the embedded receipt differs from its terminal checkpoint")
		}
		complete = &frame
	} else if errors.Is(err, errHostTerminalIncomplete) {
		scope := &hostTerminalInspection{name: name, op: held}
		scope.active.Store(true)
		defer scope.active.Store(false)
		var exists bool
		identity, exists, failure = inspectHostTerminalSource(ctx, scope, inspect)
		if failure == nil && !exists {
			failure = hostStateRemovalError("an empty or torn OP requires its exact existing source receipt")
		}
	} else {
		failure = err
	}
	if failure != nil {
		return identity, nil, failure
	}
	if err := validateHostTerminalIdentity(identity, name); err != nil {
		return identity, nil, err
	}
	if err := reference.CheckFile(held.native.root, held.native.name, held.native.file, 0600); err != nil {
		return identity, nil, err
	}
	return identity, complete, ctx.Err()
}

// This read-only negative selection grants no owner proof or completion. A
// concurrent fresh creator can make the command obsolete, but absence never
// authorises deletion and does not require creating a global lock namespace.
func existingHostTerminalDomainPresent(ctx context.Context, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(hostLockPath(name)), false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	leaf := filepath.Base(hostLockPath(name))
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0600)
	if err == nil {
		defer file.Close()
		return true, ctx.Err()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, path := range []string{HostFileKey(name), leaf + ".who"} {
		if _, err := root.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return false, hostStateRemovalError("local host state exists without a resumable OP witness")
		}
	}
	return false, ctx.Err()
}
