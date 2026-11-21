//go:build !linux

package host

import "context"

func (*Holder) quiesceOwnedMainV2(context.Context) (*ownedMainPauseV2, error) {
	return nil, sessionLeaseError("original main quiescence needs a supported Linux creation owner")
}
func (*ownedMainPauseV2) CheckPaused(context.Context) error {
	return sessionLeaseError("the original main has no supported native pause observation")
}
func (*ownedMainPauseV2) Restore(context.Context) error {
	return sessionLeaseError("the original main has no supported native restoration owner")
}
