//go:build linux

package host

import "context"

func (main *ManagerCustodyMainV1) checkManagerStopOwnerV1(ctx context.Context, owner *OwnedSupervisorLease) error {
	if main == nil || main.owner != owner || main.runtime == nil {
		return sessionLeaseError("the persistent stop source has no actual original runtime owner")
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	return main.checkLiveV1(ctx)
}

func (main *ManagerCustodyMainV1) retainManagerStopOwnerV1(ctx context.Context, owner *OwnedSupervisorLease) error {
	return main.RetainForSameRequestStopV1(ctx, owner)
}
