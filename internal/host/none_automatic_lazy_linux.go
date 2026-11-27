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

func stopLazyNoneForGateV1(ctx context.Context, operation *SessionOperation, cold *NoneColdResumeV1) error {
	if cold == nil || cold.automatic == nil || cold.sameBoot {
		return sessionLeaseError("lazy unwalled restore has no actual changed-boot gate-bound continuation")
	}
	return operation.withCurrentRead(ctx, func(owned context.Context) error {
		return withPlacement(owned, false, func(control *placementControl) error {
			if err := cold.Check(owned, operation); err != nil {
				return err
			}
			primary, err := operation.LoadContext(owned)
			if err != nil {
				return err
			}
			working, err := sandboxWasWorkingV1(primary)
			if err != nil {
				return err
			}
			if working {
				return sessionLeaseError("the unwalled working claim changed during lazy restore")
			}
			members, err := SandboxMembers(primary.ID)
			if err != nil {
				return err
			}
			at := time.Now().UTC()
			for _, member := range members {
				if err := writeLazyNoneMemberV1(owned, operation, cold, control, member, at); err != nil {
					return err
				}
			}
			return writeLazyNoneRecordV1(owned, operation, cold, operation.native.root, primary, nil, at)
		}, false)
	})
}

func writeLazyNoneMemberV1(ctx context.Context, operation *SessionOperation, cold *NoneColdResumeV1, control *placementControl, saved *Manifest, at time.Time) (failure error) {
	receipt, ok := control.MemberRoots[saved.ID]
	if !ok || receipt.Primary != operation.Primary() {
		return memberError("the lazy unwalled restore lost its original member-root receipt")
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(saved.ID), false)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, root.Close()) }()
	if err := errors.Join(cold.Check(ctx, operation), receipt.PrimaryRef.matchesRoot(operation.native.root), receipt.Directory.matchesRoot(root), checkNamedPlacementRoot(root)); err != nil {
		return err
	}
	current, err := readMemberRootManifestV1(root, saved.ID, receipt)
	if err != nil || current == nil || !current.CreatedAt.Equal(saved.CreatedAt) || !IsNoneSupervisor(current) {
		return errors.Join(memberError("the original lazy unwalled member changed"), err)
	}
	return writeLazyNoneRecordV1(ctx, operation, cold, root, current, &receipt, at)
}

func writeLazyNoneRecordV1(ctx context.Context, operation *SessionOperation, cold *NoneColdResumeV1, root *os.Root, saved *Manifest, receipt *memberRootReceiptV1, at time.Time) error {
	check := func() error {
		if err := cold.Check(ctx, operation); err != nil {
			return err
		}
		if receipt == nil {
			return operation.native.rootRef.CheckRoot(root)
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
	next := CloneManifest(*saved)
	next.Armed, next.AutomaticRestore = false, nil
	if !saved.Ended() {
		next.Life = &Life{Phase: LifeStopped, End: &LifeEnd{Kind: EndStop, Reason: "idle after a host restart", At: at}}
		next.Status = Status{State: StateStopped, Source: "restore", Since: at, Reason: "idle after a host restart"}
	}
	next.UpdatedAt = at
	raw, err := next.MarshalJSON()
	if err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	_, err = writeOwnedMetadataIntent(root, "manifest.json", append(raw, '\n'), before, func(os.FileInfo, string) error { return check() })
	if err != nil {
		return err
	}
	if receipt == nil {
		return cold.captureMetadataV1(ctx, append(raw, '\n'))
	}
	return check()
}
