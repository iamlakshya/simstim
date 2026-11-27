//go:build !linux

package host

import "context"

func withPurgeAuxiliaryContextV1(ctx context.Context, _ *purgeOwnerV1, _ *SessionOperation, _ *StoppedSessionV1) (context.Context, error) {
	return ctx, sessionLeaseError("the actual purge auxiliary source requires Linux")
}

// An absent purge context adds no exception to ordinary placement. A native
// purge exception has no producer on this platform and can never clear drain.
func checkPurgeAuxiliaryAdmissionV1(ctx context.Context) error { return ctx.Err() }
func checkPurgeAuxiliaryContextControlV1(ctx context.Context, control *placementControl) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if control != nil && control.Drain == "" {
		return nil
	}
	return UserError("draining", "this host is draining for removal", "Finish the original removal before starting another auxiliary operation.")
}
func checkPurgeAuxiliaryAdmissionControlV1(ctx context.Context, control *placementControl) error {
	return checkPurgeAuxiliaryContextControlV1(ctx, control)
}
