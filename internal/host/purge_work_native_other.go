//go:build !linux

package host

import "context"

func CheckPurgeUnsavedForOperationV1(context.Context, *SessionOperation, *StoppedSessionV1) error {
	return purgeWorkUnknownV1("the original purge reader requires a supported Linux native stopped owner")
}
