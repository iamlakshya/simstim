//go:build !linux

package host

import "context"

func (*OwnedSupervisorLease) checkOriginalTransientManagerStopV1(context.Context) error {
	return sessionLeaseError("native transient stop retention requires the original Linux manager")
}
