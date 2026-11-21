//go:build !linux

package host

import "context"

type noneStorageSubjectV1 struct{}

func beginNoneStoredDeleteV1(context.Context, *SessionOperation, *noneDeleteInvocationV1) (*noneStorageSubjectV1, error) {
	return nil, sessionLeaseError("the original None storage subject requires its Linux host")
}
func (*noneStorageSubjectV1) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("the original None storage subject is unavailable")
}
func (*noneStorageSubjectV1) checkNativeAfterRemovalV1(context.Context, *SessionOperation) error {
	return sessionLeaseError("the original None storage subject is unavailable")
}
func (*noneStorageSubjectV1) Close() error { return nil }

func (*noneStorageSubjectV1) CloseDiagnosticsV4() error { return nil }
