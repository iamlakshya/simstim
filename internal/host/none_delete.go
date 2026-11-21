package host

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A primary none session uses its distinct actual N5 stop producer before the
// existing metadata/worktree deletion rules. It never acquires a second OP.
func deleteNonePrimaryV1(id string, name *string, members *[]string, result *DeleteResult) error {
	operation, err := BeginSessionOperation(context.Background(), id, 0)
	if err != nil {
		return err
	}
	defer operation.Close()
	current, err := operation.Load()
	if err != nil {
		return err
	}
	if !IsNoneSupervisor(current) || current.SandboxOf != "" {
		return sessionLeaseError("the original unwalled deletion source changed")
	}
	if name != nil && (renameUnsaved(id, current) || current.Name != *name) {
		return UserError("session_renamed", "the unwalled session's name changed before deletion", "List the host's sessions and select the session again.")
	}
	children, err := checkDeleteMembers(current, members)
	if err != nil {
		return err
	}
	stopped, err := StopNoneSessionForOperationV1(operation.ownerContext, operation)
	if err != nil {
		return err
	}
	defer stopped.Close()
	if err := stopped.Check(context.Background(), operation); err != nil {
		return err
	}
	return deleteNoneMetadataV1(operation, stopped, current, children, result)
}

type noneDeleteRootV1 struct {
	id      string
	root    *os.Root
	created os.FileInfo
}

func deleteNoneMetadataV1(operation *SessionOperation, stopped *StoppedNoneSessionV1, current *Manifest, children []*Manifest, result *DeleteResult) (returned error) {
	ctx := context.Background()
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	parent, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if err != nil {
		return err
	}
	defer parent.Close()
	pins := make([]noneDeleteRootV1, 0, len(children)+1)
	defer func() {
		for _, pin := range pins {
			if pin.root != operation.native.root {
				returned = errors.Join(returned, pin.root.Close())
			}
		}
	}()
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
			if err := receipt.Directory.matchesRoot(root); err != nil {
				root.Close()
				return err
			}
			manifest, err := readMemberRootManifestV1(root, child.ID, receipt)
			if err != nil || manifest == nil {
				root.Close()
				return errors.Join(sessionLeaseError("the original unwalled member metadata changed"), err)
			}
			created, err := root.Stat(".")
			if err != nil {
				root.Close()
				return err
			}
			pins = append(pins, noneDeleteRootV1{id: child.ID, root: root, created: created})
		}
		return nil
	}, false); err != nil {
		return err
	}
	created, err := operation.native.root.Stat(".")
	if err != nil {
		return err
	}
	pins = append(pins, noneDeleteRootV1{id: operation.Primary(), root: operation.native.root, created: created})
	first, err := openOwnedSessionFileLease(ctx, operation.Primary(), firstLockFile, renameWait)
	if err != nil {
		return err
	}
	defer first.Close()
	if err := operation.native.rootRef.CheckRoot(first.root); err != nil {
		return err
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	if current != nil {
		var kept *proto.Error
		result.Worktree, kept = removeWorktree(current.Git)
		if kept != nil {
			result.WorktreeCode, result.WorktreeHint = kept.Code, kept.Hint
		}
	}
	for _, pin := range pins {
		named, err := parent.Lstat(pin.id)
		if err != nil || !os.SameFile(pin.created, named) {
			return errors.Join(sessionLeaseError("the original unwalled metadata directory changed before cleanup"), err)
		}
		reader, err := pin.root.Open(".")
		if err != nil {
			return err
		}
		for {
			entries, readErr := reader.ReadDir(64)
			for _, entry := range entries {
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
		named, err = parent.Lstat(pin.id)
		if err != nil || !os.SameFile(pin.created, named) {
			return errors.Join(sessionLeaseError("the unwalled cleanup will not unlink a replacement directory"), err)
		}
		if err := parent.Remove(pin.id); err != nil {
			return err
		}
	}
	if err := stopped.checkNativeAfterRemovalV1(ctx, operation); err != nil {
		return err
	}
	if err := withPlacement(ctx, false, func(control *placementControl) error {
		for _, pin := range pins {
			if pin.id == operation.Primary() {
				continue
			}
			receipt, known := control.MemberRoots[pin.id]
			if !known {
				continue
			}
			if receipt.Primary != operation.Primary() || !receipt.Directory.matches(pin.created) || !receipt.PrimaryRef.matches(created) {
				return sessionLeaseError("the original member creator receipt changed during unwalled cleanup")
			}
			delete(control.MemberRoots, pin.id)
		}
		return stopped.checkNativeAfterRemovalV1(ctx, operation)
	}, true); err != nil {
		return err
	}
	result.Deleted = true
	return nil
}
