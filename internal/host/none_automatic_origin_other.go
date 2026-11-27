//go:build !linux

package host

import "context"

func captureNoneAutomaticContinuationV1(context.Context, *SessionOperation, *restoreGateV1) (*NoneColdResumeV1, error) {
	return nil, sessionLeaseError("the actual automatic unwalled continuation requires a supported Linux host")
}
func (*NoneColdResumeV1) restoreFromGateV1(context.Context, *SessionOperation) ([]string, string, error) {
	return nil, "", sessionLeaseError("the actual automatic unwalled continuation requires a supported Linux host")
}
func stopLazyNoneForGateV1(context.Context, *SessionOperation, *NoneColdResumeV1) error {
	return sessionLeaseError("the actual unwalled lazy restore requires a supported Linux host")
}
