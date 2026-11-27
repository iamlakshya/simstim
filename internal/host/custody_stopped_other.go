//go:build !linux

package host

import "context"

type managerStoppedV1 struct{}

func stopIntoManagerCustodyV1(context.Context, *SessionOperation) (*managerStoppedV1, bool, error) {
	return nil, false, nil
}
func (*managerStoppedV1) Close() error { return nil }
func (*managerStoppedV1) stopResultV1(context.Context, string, error) (*SessionInfo, error) {
	return nil, sessionLeaseError("native manager custody requires a Linux service main")
}
