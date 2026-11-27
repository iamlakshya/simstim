//go:build !linux

package host

import "context"

func checkManagedNoneRSSStartLockedV1(_ context.Context, operation *SessionOperation, _ *Manifest, _ *placementControl) (bool, error) {
	if operation != nil && operation.noneCold != nil {
		return true, sessionLeaseError("the original managed None RSS owner needs a supported Linux host")
	}
	return false, nil
}
