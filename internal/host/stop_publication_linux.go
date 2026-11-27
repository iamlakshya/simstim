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

// publishRequestedStopV1 consumes only the SAME original operation and real
// physical whole-stop owner. Nonzero exit diagnostics stay diagnostic data;
// nothing here certifies CleanExit, kept capture or a later cold start.
func publishRequestedStopV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1) (*Manifest, error) {
	if err := stopped.Check(ctx, operation); err != nil {
		return nil, err
	}
	var published *Manifest
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
				if err := publishRequestedStopMemberV1(owned, operation, stopped, control, member, at); err != nil {
					return err
				}
			}
			if err := writeRequestedStopRecordV1(owned, operation, stopped, operation.native.root, primary, nil, at); err != nil {
				return err
			}
			published, err = operation.LoadContext(owned)
			return errors.Join(err, stopped.Check(owned, operation))
		}, false)
	})
	if err != nil {
		return nil, err
	}
	return published, nil
}

func publishRequestedStopMemberV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1, control *placementControl, expected *Manifest, at time.Time) (returned error) {
	receipt, ok := control.MemberRoots[expected.ID]
	if !ok || receipt.Primary != operation.Primary() {
		return memberError("the explicit stop has no original member directory receipt")
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(expected.ID), false)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	if err := errors.Join(stopped.Check(ctx, operation), receipt.PrimaryRef.matchesRoot(operation.native.root), receipt.Directory.matchesRoot(root), checkNamedPlacementRoot(root)); err != nil {
		return err
	}
	member, err := readMemberRootManifestV1(root, expected.ID, receipt)
	if err != nil || member == nil || !member.CreatedAt.Equal(expected.CreatedAt) {
		return errors.Join(memberError("the original stopped member changed"), err)
	}
	return writeRequestedStopRecordV1(ctx, operation, stopped, root, member, &receipt, at)
}

func writeRequestedStopRecordV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1, root *os.Root, saved *Manifest, receipt *memberRootReceiptV1, at time.Time) error {
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
