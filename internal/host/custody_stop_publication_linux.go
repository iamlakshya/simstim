//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// publishRequestedManagerStopV1 is distinct from ordinary and None publishers.
// It consumes only this original manager-stopped source. Shared helpers below
// project DATA and compare held manifest inodes; they supply no stop authority.
func publishRequestedManagerStopV1(ctx context.Context, operation *SessionOperation, stopped *managerStoppedV1) (*Manifest, error) {
	if err := stopped.Check(ctx, operation); err != nil {
		return nil, err
	}
	var result *Manifest
	err := operation.withCurrentRead(ctx, func(owned context.Context) error {
		return withPlacement(owned, true, func(control *placementControl) error {
			if err := stopped.Check(owned, operation); err != nil {
				return err
			}
			primary, err := operation.LoadContext(owned)
			if err != nil {
				return err
			}
			members, err := SandboxMembers(primary.ID)
			if err != nil {
				return err
			}
			at := time.Now().UTC()
			for _, member := range members {
				if err := publishManagerStopMemberV1(owned, operation, stopped, control, member, at); err != nil {
					return err
				}
			}
			if err := writeManagerStopRecordV1(owned, operation, stopped, operation.native.root, primary, nil, at); err != nil {
				return err
			}
			result, err = operation.LoadContext(owned)
			return errors.Join(err, stopped.Check(owned, operation))
		}, false)
	})
	return result, err
}

func publishManagerStopMemberV1(ctx context.Context, operation *SessionOperation, stopped *managerStoppedV1, control *placementControl, expected *Manifest, at time.Time) (failure error) {
	receipt, ok := control.MemberRoots[expected.ID]
	if !ok || receipt.Primary != operation.Primary() {
		return memberError("the manager stop has no original member-root receipt")
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(expected.ID), false)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, root.Close()) }()
	if err := errors.Join(stopped.Check(ctx, operation), receipt.PrimaryRef.matchesRoot(operation.native.root), receipt.Directory.matchesRoot(root), checkNamedPlacementRoot(root)); err != nil {
		return err
	}
	member, err := readMemberRootManifestV1(root, expected.ID, receipt)
	if err != nil || member == nil || !member.CreatedAt.Equal(expected.CreatedAt) {
		return errors.Join(memberError("the original manager-stopped member changed"), err)
	}
	return writeManagerStopRecordV1(ctx, operation, stopped, root, member, &receipt, at)
}

func writeManagerStopRecordV1(ctx context.Context, operation *SessionOperation, stopped *managerStoppedV1, root *os.Root, saved *Manifest, receipt *memberRootReceiptV1, at time.Time) error {
	check := func() error {
		if err := stopped.Check(ctx, operation); err != nil {
			return err
		}
		if receipt == nil {
			if saved.ID != operation.Primary() || saved.SandboxOf != "" {
				return snapshotChanged()
			}
			return operation.native.rootRef.CheckRoot(root)
		}
		if receipt.Primary != operation.Primary() || saved.SandboxOf != operation.Primary() || !saved.CreatedAt.Equal(receipt.CreatedAt) {
			return snapshotChanged()
		}
		return errors.Join(receipt.PrimaryRef.matchesRoot(operation.native.root), receipt.Directory.matchesRoot(root), checkNamedPlacementRoot(root))
	}
	if err := check(); err != nil {
		return err
	}
	file, before, err := openExactStoppedMetadataV1(root, saved)
	if err != nil {
		return err
	}
	defer file.Close()
	next, err := explicitStopProjectionV1(saved, at)
	if err != nil {
		return err
	}
	raw, err := next.MarshalJSON()
	if err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	_, err = writeOwnedMetadataIntent(root, "manifest.json", append(raw, '\n'), before, func(os.FileInfo, string) error { return check() })
	return errors.Join(err, check())
}

func (stopped *managerStoppedV1) stopResultV1(ctx context.Context, id string, stopErr error) (*SessionInfo, error) {
	operation := stopped.capture.operation
	publication, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	primary, err := publishRequestedManagerStopV1(publication, operation, stopped)
	if err != nil {
		return nil, errors.Join(stopErr, err)
	}
	if stopErr != nil || ctx.Err() != nil {
		return nil, errors.Join(stopErr, ctx.Err())
	}
	selected := primary
	if id != primary.ID {
		selected, err = LoadManifest(id)
		if err != nil {
			return nil, err
		}
		if selected.ID != id || selected.SandboxOf != primary.ID {
			return nil, snapshotChanged()
		}
	}
	if selected.Life == nil || selected.Life.Phase != LifeStopped || selected.Life.End == nil || selected.Life.End.Kind != EndStop || selected.Status.State != StateStopped {
		return nil, UserError("stop_failed", "the manager-stopped sandbox has no completed stop record", "Inspect its original native custody with host doctor before retrying.")
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return nil, err
	}
	row := publicationInfoFrom(selected, false, primary)
	return &row, nil
}
