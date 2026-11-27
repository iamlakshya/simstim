//go:build !linux

package host

import "context"

type purgeStoppedCleanupKeyV1 struct{}

func DestroyStoppedSessionResourcesV1(context.Context, *SessionOperation, *StoppedSessionV1) error {
	return sessionLeaseError("the actual native stopped storage consumer requires Linux")
}

func finishStoppedSessionMetadataV1(context.Context, *purgeOwnerV1, *SessionOperation, *StoppedSessionV1) error {
	return sessionLeaseError("the actual native terminal session cleanup requires Linux")
}
