package client

// NewPurgeGrantRequest owns one original removal and its cleanup-only native
// continuation. It cannot select values, start a sandbox or run a root plan.
func NewPurgeGrantRequest() *GrantRequest {
	return &GrantRequest{purpose: grantPurge, operation: grantOpPurge}
}
