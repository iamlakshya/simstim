//go:build !linux

package host

import "context"

func stopNoneContextForOperationV1(context.Context, string, *SessionOperation) (*SessionInfo, error) {
	return nil, sessionLeaseError("the actual N5 unwalled stop requires a supported Linux host")
}
