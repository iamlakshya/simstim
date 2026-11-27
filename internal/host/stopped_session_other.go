//go:build !linux

package host

import "context"

// StoppedSessionV1 has no native producer on unsupported operating systems.
// Its zero value cannot supply physical stopped ownership.
type StoppedSessionV1 struct{}

func StopSessionForOperationV1(context.Context, *SessionOperation) (*StoppedSessionV1, error) {
	return nil, sessionLeaseError("a whole-unit native stop source requires a supported Linux host")
}
func (*StoppedSessionV1) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("a whole-unit native stop source requires a supported Linux host")
}
func (*StoppedSessionV1) Close() error { return nil }
