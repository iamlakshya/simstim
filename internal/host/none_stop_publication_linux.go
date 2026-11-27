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

// publishRequestedNoneStopV1 consumes only the SAME actual N5 completion.
// It never casts None into a whole-unit, VM or ordinary stopped source.
// The DATA projection preserves any genuine NoneCleanup field from the
// supervisor final save. A stop DTO cannot create or repair that field.
func publishRequestedNoneStopV1(ctx context.Context, operation *SessionOperation, stopped *StoppedNoneSessionV1) (*Manifest, error) {
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
				if err := publishRequestedNoneStopMemberV1(owned, operation, stopped, control, member, at); err != nil {
					return err
				}
			}
			if err := writeRequestedNoneStopRecordV1(owned, operation, stopped, operation.native.root, primary, nil, at); err != nil {
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

func publishRequestedNoneStopMemberV1(ctx context.Context, operation *SessionOperation, stopped *StoppedNoneSessionV1, control *placementControl, expected *Manifest, at time.Time) (returned error) {
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
	return writeRequestedNoneStopRecordV1(ctx, operation, stopped, root, member, &receipt, at)
}

func writeRequestedNoneStopRecordV1(ctx context.Context, operation *SessionOperation, stopped *StoppedNoneSessionV1, root *os.Root, saved *Manifest, receipt *memberRootReceiptV1, at time.Time) error {
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

// stopNoneContextForOperationV1 is the public stop adapter for a concrete N5
// creator. Its caller retains the SAME original OP until cleanup has joined.
func stopNoneContextForOperationV1(ctx context.Context, id string, operation *SessionOperation) (_ *SessionInfo, failure error) {
	stopped, stopErr := StopNoneSessionForOperationV1(ctx, operation)
	if stopped != nil {
		defer func() { failure = errors.Join(failure, stopped.Close()) }()
	}
	if stopped == nil {
		if stopErr == nil {
			stopErr = UserError("stop_failed", "the unwalled stop returned no original N5 completion", "Keep the original session and inspect its creator before retrying stop.")
		}
		return nil, stopErr
	}
	publication, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	primary, err := publishRequestedNoneStopV1(publication, operation, stopped)
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
		return nil, UserError("stop_failed", "the unwalled creator has no completed stop record", "Inspect its original N5 cleanup before preparing another resume.")
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return nil, err
	}
	row := publicationInfoFrom(selected, false, primary)
	return &row, nil
}
