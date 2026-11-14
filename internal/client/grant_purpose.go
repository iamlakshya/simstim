package client

import "context"

// Purposes are closed and immutable before a request enters a context.
// Local review can share admission rules without donating send approvals.
type grantPurpose uint8

const (
	grantRemote grantPurpose = iota
	grantLocalReview
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
	if r := grantRequestOf(ctx); r != nil && requestGrantPurpose(r) != grantRemote {
		return grantPurposeError()
	}
	return nil
}

func grantPurposeError() error {
	return &UserError{Msg: "this local review request cannot send values or publish session changes",
		Hint: "Start a separate session command with its own grant request.", Code: "grant_purpose", Kind: KindRefused}
}
