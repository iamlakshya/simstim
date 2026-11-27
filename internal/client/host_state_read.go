package client

import "context"

// withHostStateReadContext observes the actual local read, never an earlier
// remote payload. Existing tokens are retained even when expired, so this
// helper cannot freshen a failed producer or publication-only context.
func withHostStateReadContext(ctx context.Context, h Host, run func(context.Context) error) error {
	if ctx.Value(hostStateObservationKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil {
		return run(ctx)
	}
	owned, unlock, err := LockHost(ctx, h.Name, "host state read")
	if err != nil {
		return err
	}
	defer unlock()
	observation, err := ObserveHostStateContext(owned, h, "")
	if err != nil {
		return err
	}
	observation.role = hostStateRead
	return run(WithHostStateObservation(owned, observation))
}
