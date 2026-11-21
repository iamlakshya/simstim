//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"slices"
)

// Member IDs add refusal targets only. Each comes from the original rooted
// member creator receipt, never a guest PID, row or caller-supplied member DTO.
func captureNoneColdMemberIDsV1(ctx context.Context, operation *SessionOperation) ([]string, error) {
	var ids []string
	err := withPlacement(ctx, false, func(control *placementControl) error {
		for id, receipt := range control.MemberRoots {
			if receipt.Primary != operation.Primary() {
				continue
			}
			if err := receipt.PrimaryRef.matchesRoot(operation.native.root); err != nil {
				return err
			}
			root, err := cachefs.OpenPrivateRoot(paths.SessionDir(id), false)
			if err != nil {
				return err
			}
			if err := receipt.Directory.matchesRoot(root); err != nil {
				return errors.Join(err, root.Close())
			}
			m, err := readMemberRootManifestV1(root, id, receipt)
			if err != nil {
				return errors.Join(err, root.Close())
			}
			if m == nil || !IsNoneSupervisor(m) || m.SandboxOf != operation.Primary() || m.RuntimeManifest != nil || len(m.VMNeedV1) != 0 || m.Files != nil {
				return errors.Join(sessionLeaseError("an original None member changed its execution role"), root.Close())
			}
			if err := root.Close(); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return ctx.Err()
	}, false)
	if err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
}
