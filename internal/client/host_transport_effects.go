package client

import "context"

// Existing explicit lifecycle APIs keep their own operation. Within an actual
// Dial or registration, every effect retains that original typed provenance.
func checkHostTransportAction(ctx context.Context, h Host) error {
	if attempt, _ := ctx.Value(hostTransportAttemptKey{}).(*hostTransportAttempt); attempt != nil {
		return checkHostTransportAttempt(ctx, attempt, h)
	}
	if observation, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); observation != nil && observation.role == hostStateRegistration {
		_, err := captureHostStateObservation(ctx, h)
		return err
	}
	if restriction, _ := ctx.Value(hostTransportRestrictionKey{}).(*hostTransportRestrictionV1); restriction != nil {
		return restriction.expected.check(ctx, h)
	}
	return ctx.Err()
}

func checkHostTransportLifecycleWake(ctx context.Context, h Host) error {
	if err := checkHostTransportAction(ctx, h); err != nil {
		return err
	}
	if attempt, _ := ctx.Value(hostTransportAttemptKey{}).(*hostTransportAttempt); attempt != nil && attempt.kind == hostTransportProvisional {
		return hostStateRemovalError("an unsaved read-only transport cannot start or resume a machine")
	}
	return nil
}
