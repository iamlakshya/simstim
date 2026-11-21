//go:build !linux

package host

import "context"

type StoppedNoneSessionV1 struct{}

func StopNoneSessionForOperationV1(context.Context, *SessionOperation) (*StoppedNoneSessionV1, error) {
	return nil, sessionLeaseError("the native unwalled stop producer needs a supported Linux host")
}
func (*StoppedNoneSessionV1) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("the original native unwalled stop producer is unavailable")
}
func (*StoppedNoneSessionV1) Close() error { return nil }
func (*StoppedNoneSessionV1) checkNativeAfterRemovalV1(context.Context, *SessionOperation) error {
	return sessionLeaseError("the original native unwalled stop producer is unavailable")
}
