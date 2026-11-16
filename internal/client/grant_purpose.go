package client

import "context"

// Purposes are closed and immutable before a request enters a context.
// Local review can share admission rules without donating send approvals.
type grantPurpose uint8

const (
	grantRemote grantPurpose = iota
	grantLocalReview
	grantLocalForge
	grantLocalRoot
)

func requestGrantPurpose(r *GrantRequest) grantPurpose {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.purpose
}

func requireRemoteGrantRequest(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r := grantRequestOf(ctx); r != nil {
		r.mu.Lock()
		lifetimeErr := r.lifetimeErrorLocked()
		purpose := r.purpose
		r.mu.Unlock()
		if lifetimeErr != nil {
			return lifetimeErr
		}
		if purpose != grantRemote {
			return grantPurposeError()
		}
	}
	return nil
}

func grantPurposeError() error {
	return &UserError{Msg: "this local request belongs to another permission purpose",
		Hint: "Start a separate command with its own purpose and grant request.", Code: "grant_purpose", Kind: KindRefused}
}
