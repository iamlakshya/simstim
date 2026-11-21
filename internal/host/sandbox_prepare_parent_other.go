//go:build !linux

package host

func (*OwnedSupervisorLease) CaptureSandboxPrepareParentCleanupV1(*SessionOperation) (*SandboxPrepareParentCleanupV1, error) {
	return nil, sessionLeaseError("native prepare parent capture requires the Linux host helper")
}

func (*SandboxPrepareParentCleanupV1) RemoveAfterParentExitV1() (bool, error) {
	return false, sessionLeaseError("native prepare parent cleanup requires the Linux host helper")
}

func (*SandboxPrepareParentCleanupV1) checkNativeV1() error {
	return sessionLeaseError("native prepare parent cleanup requires the Linux host helper")
}

func (*SandboxPrepareParentCleanupV1) parentExitedV1() (bool, error) {
	return false, sessionLeaseError("native prepare parent cleanup requires the Linux host helper")
}
