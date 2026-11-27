package client

import "context"

// captureHostSelectionSource observes a genuine local value/name selection.
// It supplies only current saved membership Source, never checked identity or
// remote payload authority. Existing stale tokens are refused, never refreshed.
func captureHostSelectionSource(ctx context.Context, h Host) (source hostStateSource, failure error) {
	if ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil {
		return source, hostStateRemovalError("local selection Source must be captured before local writer callbacks")
	}
	failure = withHostStateReadContext(ctx, h, func(owned context.Context) error {
		o, _ := owned.Value(hostStateObservationKey{}).(*HostStateObservation)
		if o == nil || (o.role != hostStateSaved && o.role != hostStateRead) || !SameEntry(o.prospective, h) {
			return hostStateRemovalError("local selection has no exact saved-host read observation")
		}
		return withHostMembershipContext(owned, func(locked context.Context) error {
			if err := o.checkMembership(locked); err != nil {
				return err
			}
			source = o.source
			return nil
		})
	})
	return source, failure
}
