package client

// Caller holds r.mu. This controls only logical request lifetime; it grants
// no Source, value, publication or native process authority.
func (r *GrantRequest) lifetimeErrorLocked() error {
	if r.ending || r.preparationClosed || r.requestContext == nil {
		return grantOperationError()
	}
	return r.requestContext.Err()
}
