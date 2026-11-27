package host

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

type noneDeleteRootV1 struct {
	id        string
	root      *os.Root
	created   os.FileInfo
	reference cachefs.Reference
	unlinked  bool
}

// The actual selected roots/parent are retained across partial metadata
// unlink and parent-sync retries, never reconstructed from a missing name.
type noneMetadataCleanupV1 struct {
	retirement                                     nativeDescriptorRetirementsV1
	firstRetirement                                noneMetadataFirstRetirementV1
	mu                                             sync.Mutex
	self                                           *noneMetadataCleanupV1
	operation                                      *SessionOperation
	source                                         *noneDeleteSourceV1
	parent                                         *os.Root
	parentRef                                      cachefs.Reference
	pins                                           []noneDeleteRootV1
	primary                                        os.FileInfo
	first                                          *sessionFileLease
	current                                        *Manifest
	ready, worktreeDone, started, complete, closed bool
	worktree                                       DeleteResult
}

func captureNoneMetadataCleanupV1(operation *SessionOperation, source *noneDeleteSourceV1, current *Manifest, children []*Manifest) (result *noneMetadataCleanupV1, returned error) {
	ctx := context.Background()
	if err := source.Check(ctx, operation); err != nil {
		return nil, err
	}
	parent, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if err != nil {
		return nil, err
	}
	plan := &noneMetadataCleanupV1{operation: operation, source: source, parent: parent, current: current}
	plan.self = plan
	complete := false
	defer func() {
		if !complete {
			closeErr := plan.Close()
			returned = errors.Join(returned, plan.closeDiagnosticsV1())
			if closeErr != nil {
				// A partial original owner must be returned before error so
				// its SAME roots/OP can settle; never discard it as nil.
				result = plan
				returned = errors.Join(returned, closeErr)
			}
		}
	}()
	plan.parentRef, err = cachefs.CaptureRoot(parent)
	if err != nil {
		return nil, err
	}
	if err := withPlacement(ctx, false, func(control *placementControl) error {
		for _, child := range children {
			receipt, known := control.MemberRoots[child.ID]
			if !known || receipt.Primary != operation.Primary() {
				return sessionLeaseError("the unwalled member has no original directory creator receipt")
			}
			if err := receipt.PrimaryRef.matchesRoot(operation.native.root); err != nil {
				return err
			}
			root, err := cachefs.OpenPrivateChild(parent, child.ID, false)
			if err != nil {
				return err
			}
			// The actual plan owns this root before any later check can fail.
			if err := plan.addRootV1(child.ID, root); err != nil {
				return err
			}
			if err := receipt.Directory.matchesRoot(root); err != nil {
				return err
			}
			manifest, err := readMemberRootManifestV1(root, child.ID, receipt)
			if err != nil || manifest == nil {
				return errors.Join(sessionLeaseError("the original unwalled member metadata changed"), err)
			}
		}
		return nil
	}, false); err != nil {
		return nil, err
	}
	if err := plan.addRootV1(operation.Primary(), operation.native.root); err != nil {
		return nil, err
	}
	plan.primary, err = operation.native.root.Stat(".")
	if err != nil {
		return nil, err
	}
	plan.first, err = openOwnedSessionFileLease(ctx, operation.Primary(), firstLockFile, renameWait)
	if err != nil {
		return nil, err
	}
	if err := operation.native.rootRef.CheckRoot(plan.first.root); err != nil {
		return nil, err
	}
	complete = true
	plan.ready = true
	return plan, nil
}

func (plan *noneMetadataCleanupV1) addRootV1(id string, root *os.Root) error {
	plan.pins = append(plan.pins, noneDeleteRootV1{id: id, root: root})
	pin := &plan.pins[len(plan.pins)-1]
	created, err := root.Stat(".")
	if err != nil {
		return err
	}
	ref, err := cachefs.CaptureRoot(root)
	if err != nil {
		return err
	}
	pin.created, pin.reference = created, ref
	return nil
}

func (plan *noneMetadataCleanupV1) checkRootV1(ctx context.Context, pin *noneDeleteRootV1) error {
	if plan == nil || plan.self != plan || plan.closed || pin == nil {
		return sessionLeaseError("the retained None metadata cleanup ended or was copied")
	}
	if err := errors.Join(ctx.Err(), plan.parentRef.CheckRoot(plan.parent), plan.source.Check(ctx, plan.operation)); err != nil {
		return err
	}
	if pin.unlinked {
		return checkNoneOwnedDirectoryUnlinkedV1(pin.root, pin.created)
	}
	if err := pin.reference.CheckRoot(pin.root); err != nil {
		return err
	}
	named, err := plan.parent.Lstat(pin.id)
	if err != nil || !os.SameFile(pin.created, named) {
		return errors.Join(sessionLeaseError("the original unwalled metadata name changed; the replacement was preserved"), err)
	}
	return nil
}

func (plan *noneMetadataCleanupV1) Remove(ctx context.Context, result *DeleteResult) error {
	if plan == nil || plan.self != plan || ctx == nil {
		return sessionLeaseError("the actual None metadata cleanup is absent")
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.closed {
		return sessionLeaseError("the original None metadata roots closed")
	}
	if !plan.ready {
		return sessionLeaseError("the original None metadata capture is incomplete; only its owned preparation may close before a retry")
	}
	if plan.complete {
		result.Deleted = true
		return nil
	}
	if err := plan.source.Check(ctx, plan.operation); err != nil {
		return err
	}
	if !plan.worktreeDone {
		if plan.current != nil {
			var kept *proto.Error
			plan.worktree.Worktree, kept = removeWorktree(plan.current.Git)
			if kept != nil {
				plan.worktree.WorktreeCode, plan.worktree.WorktreeHint = kept.Code, kept.Hint
			}
		}
		plan.worktreeDone = true
	}
	result.Worktree, result.WorktreeCode, result.WorktreeHint = plan.worktree.Worktree, plan.worktree.WorktreeCode, plan.worktree.WorktreeHint
	owned := context.WithoutCancel(ctx)
	for i := range plan.pins {
		pin := &plan.pins[i]
		if err := plan.checkRootV1(owned, pin); err != nil {
			return err
		}
		if pin.unlinked {
			continue
		}
		reader, err := pin.root.Open(".")
		if err != nil {
			return err
		}
		for {
			entries, readErr := reader.ReadDir(64)
			for _, entry := range entries {
				if err := plan.checkRootV1(owned, pin); err != nil {
					reader.Close()
					return err
				}
				plan.started = true
				if err := pin.root.RemoveAll(entry.Name()); err != nil {
					reader.Close()
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				reader.Close()
				return readErr
			}
		}
		if err := reader.Close(); err != nil {
			return err
		}
		if err := plan.checkRootV1(owned, pin); err != nil {
			return err
		}
		plan.started = true
		if err := plan.parent.Remove(pin.id); err != nil {
			return err
		}
		pin.unlinked = true
		if err := checkNoneOwnedDirectoryUnlinkedV1(pin.root, pin.created); err != nil {
			return err
		}
	}
	if err := syncCapturedDirectory(plan.parent); err != nil {
		return err
	}
	if err := plan.source.checkNativeAfterRemovalV1(owned, plan.operation); err != nil {
		return err
	}
	if err := withPlacement(owned, false, func(control *placementControl) error {
		for _, pin := range plan.pins {
			if pin.id == plan.operation.Primary() {
				continue
			}
			receipt, known := control.MemberRoots[pin.id]
			if !known {
				continue
			}
			if receipt.Primary != plan.operation.Primary() || !receipt.Directory.matches(pin.created) || !receipt.PrimaryRef.matches(plan.primary) {
				return sessionLeaseError("the original member creator receipt changed during unwalled cleanup")
			}
			delete(control.MemberRoots, pin.id)
		}
		return plan.source.checkNativeAfterRemovalV1(owned, plan.operation)
	}, true); err != nil {
		return err
	}
	plan.complete = true
	result.Deleted = true
	return nil
}

func (plan *noneMetadataCleanupV1) Close() error {
	if plan == nil {
		return nil
	}
	if plan.self != plan {
		return sessionLeaseError("a copied None metadata cleanup cannot release its actual roots")
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.closed {
		return nil
	}
	if plan.started && !plan.complete {
		return noneDirectoryOriginErrorV1("the original None metadata roots and parent remain retained after partial deletion")
	}
	var err error
	if plan.first != nil {
		unknown := plan.retireFirstV1()
		err = errors.Join(err, unknown)
		if unknown == nil {
			plan.first = nil
		}
	}
	// Every operation on these private roots has returned under plan.mu.
	// The primary root is shared with SAME OP and is never retired here.
	for i := range plan.pins {
		pin := &plan.pins[i]
		if pin.root != nil && pin.root != plan.operation.native.root {
			roots := []*os.Root{pin.root}
			_ = plan.retirement.closeRootsV4(roots)
			unknown := plan.retirement.checkRootsV4(roots)
			err = errors.Join(err, unknown)
			if unknown == nil {
				pin.root = nil
			}
		}
	}
	if plan.parent != nil {
		roots := []*os.Root{plan.parent}
		_ = plan.retirement.closeRootsV4(roots)
		unknown := plan.retirement.checkRootsV4(roots)
		err = errors.Join(err, unknown)
		if unknown == nil {
			plan.parent = nil
		}
	}
	if err == nil {
		plan.closed = true
	}
	return err
}

// Reporting a known-retired close diagnostic never recreates native custody.
func (plan *noneMetadataCleanupV1) closeDiagnosticsV1() error {
	if plan == nil || plan.self != plan {
		return nil
	}
	return plan.retirement.diagnosticsV4()
}
