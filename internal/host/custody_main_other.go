//go:build !linux

package host

import "context"

// ManagerCustodyMainV1 has no kernel producer on non-Linux host helpers.
type ManagerCustodyMainV1 struct{}

// PrepareManagerCustodyV1 retains the existing non-Linux fixture path.
func (*OwnedSupervisorLease) PrepareManagerCustodyV1(context.Context) (*ManagerCustodyMainV1, error) {
	return nil, nil
}

// DepositAfterQuiescenceV1 cannot originate native custody on this platform.
func (*ManagerCustodyMainV1) DepositAfterQuiescenceV1(context.Context) error {
	return sessionLeaseError("native manager custody requires the original Linux service main")
}

// Close has no native copies to release on this platform.
func (*ManagerCustodyMainV1) Close() error { return nil }

// RetainForSameRequestStopV1 has no native manager configuration here.
func (*ManagerCustodyMainV1) RetainForSameRequestStopV1(context.Context, *OwnedSupervisorLease) error {
	return nil
}
