package client

import "context"

// Abort retires this request's never-attempted reservation, or its exact
// correlated terminal no-room attempt after proven native settlement.
// The owner cancels/joins its active attempt first and supplies its own cleanup
// context. Close separately releases source FDs/values. An uncertain physical
// write, changed Source, future data or a receipt retains the original witness.
func (r *GrantRequest) Abort(ctx context.Context) (bool, error) {
	if r == nil {
		return false, nil
	}
	r.mu.Lock()
	plan, fresh := r.newSecrets, r.freshChild
	r.mu.Unlock()
	if fresh != nil {
		return fresh.abort(ctx)
	}
	if handled, retired, err := abortSettledHostAttemptNewV1(ctx, r, plan); handled {
		return retired, err
	}
	if plan == nil || plan.creation == nil {
		return false, nil
	}
	plan.mu.Lock()
	if plan.retired {
		plan.mu.Unlock()
		return true, nil
	}
	if plan.busy {
		plan.mu.Unlock()
		return false, agentModeResourceError()
	}
	if plan.attempted || plan.published {
		plan.mu.Unlock()
		return false, nil
	}
	plan.mu.Unlock()
	retired, err := retireUnpublishedSandboxContext(ctx, plan.conn, plan.creation, plan)
	if retired {
		plan.mu.Lock()
		plan.retired = true
		plan.aborting = true
		plan.mu.Unlock()
	}
	return retired, err
}

func (p *newSecretPlan) holdUnpublishedSandboxRetirement(ctx context.Context, c *Conn, original *SandboxPolicy) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.conn != c || p.closed || p.busy || p.attempted || p.published || p.retired || !sameSandboxCreation(p.creation, original) {
		p.mu.Unlock()
		return nil, sandboxReceiptError()
	}
	// This terminal lease prevents later fetch/finalisation/first writer
	// admission until the sole lower transaction and native close join.
	p.aborting = true
	return p.mu.Unlock, nil
}

func (p *newSecretPlan) markPublicationAttempt() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.aborting || p.retired || p.published || !p.busy {
		return grantOperationError()
	}
	p.attempted = true
	return nil
}

type newPlanPublicationKey struct{}
