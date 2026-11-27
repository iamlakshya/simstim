//go:build !linux

package host

func (*OwnedSupervisorLease) ManagerCustodyReleasedV1() bool { return false }
func (*OwnedSupervisorLease) CheckManagerCustodyCancelV1(*StartupLineV1) error {
	return sessionLeaseError("the original manager custody cancellation needs a supported Linux host")
}
