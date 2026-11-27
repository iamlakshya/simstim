//go:build !linux

package host

import "context"

func checkColdRestartReservationLockedV1(context.Context, *SessionOperation, *Manifest) (bool, error) {
	return false, nil
}
