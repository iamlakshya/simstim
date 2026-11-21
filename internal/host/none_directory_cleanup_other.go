//go:build !linux

package host

import "context"

type noneDirectoryCleanupV1 struct{}

func (*noneDirectoryCleanupV1) closeDiagnosticsV1() error { return nil }
func (*noneDirectoryCleanupV1) Close() error              { return nil }
func (*noneDirectoryCleanupV1) mutationStartedV1() bool   { return false }
func (*noneDeleteSourceV1) cleanupOriginalDirectoryV1(context.Context) error {
	return sessionLeaseError("the original None storage cleanup requires its Linux host")
}
func (*NoneColdResumeV1) cleanupPreviousNoneDirectoryV1(context.Context) error {
	return sessionLeaseError("the original None directory cleanup requires its Linux host")
}
