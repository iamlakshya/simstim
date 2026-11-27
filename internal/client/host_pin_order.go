package client

import "context"

// checkHostPinOrder belongs at the start of canonical lockPinAs, before any
// folder, lock file or wait. Pin precedes membership in the canonical order.
func checkHostPinOrder(ctx context.Context) error {
	if ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil ||
		ctx.Value(hostStateInspectionKey{}) != nil || ctx.Value(hostTerminalInspectionKey{}) != nil {
		return hostStateRemovalError("pin acquisition cannot begin inside local membership or owner inspection")
	}
	return ctx.Err()
}
