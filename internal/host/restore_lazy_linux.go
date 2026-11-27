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

// stopLazyRestoreV1 never stops or adopts a current process. It retains the
// existing native vacancy from the real prior boot, original holder lock,
// native manager and both empty unit/cgroup observations through publication.
func stopLazyRestoreV1(operation *SessionOperation) (returned error) {
	if operation == nil || operation.ownerContext == nil {
		return sessionLeaseError("lazy restore has no original owning operation")
	}
	ctx := operation.ownerContext
	vacancy, err := captureAuxiliaryVacancyV1(ctx, operation)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, vacancy.Close()) }()
	return operation.withCurrentRead(ctx, func(owned context.Context) error {
		return withPlacement(owned, false, func(control *placementControl) error {
			if err := vacancy.Check(owned); err != nil {
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
			// A working claim narrows this idle decision; it supplies no start
			// authority. A fresh restoreOne must select the new command set.
			for _, member := range append([]*Manifest{primary}, members...) {
				if !member.Ended() && member.Guest != nil && member.Guest.State == StateWorking {
					return memberError("the sandbox's last working claim changed during lazy restore")
				}
			}
			at := time.Now().UTC()
			for _, member := range members {
				if err := stopLazyMemberV1(owned, operation, vacancy, control, member, at); err != nil {
					return err
				}
			}
			if err := vacancy.Check(owned); err != nil {
				return err
			}
			return writeLazyStoppedV1(operation.native.root, primary, at, func() error { return vacancy.Check(owned) })
		}, false)
	})
}

func stopLazyMemberV1(ctx context.Context, operation *SessionOperation, vacancy *auxiliaryVacancyV1, control *placementControl, expected *Manifest, at time.Time) (returned error) {
	receipt, ok := control.MemberRoots[expected.ID]
	if !ok || receipt.Primary != operation.Primary() {
		return memberError("lazy restore has no original member directory receipt")
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(expected.ID), false)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	check := func() error {
		return errors.Join(ctx.Err(), vacancy.Check(ctx), receipt.PrimaryRef.matchesRoot(operation.native.root), receipt.Directory.matchesRoot(root))
	}
	if err := check(); err != nil {
		return err
	}
	member, err := readMemberRootManifestV1(root, expected.ID, receipt)
	if err != nil || member == nil || !member.CreatedAt.Equal(expected.CreatedAt) {
		return errors.Join(memberError("the original lazy restore member changed"), err)
	}
	return writeLazyStoppedV1(root, member, at, check)
}

// The shared rooted metadata writer supplies inode CAS and directory sync.
// No command, policy, runtime identity or persistent workspace is replaced.
func writeLazyStoppedV1(root *os.Root, saved *Manifest, at time.Time, check func() error) error {
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
	return errors.Join(err, check())
}
