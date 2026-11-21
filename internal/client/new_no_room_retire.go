package client

import "context"

// H5 invokes this before the older never-attempted Abort branch. handled is
// true only for this actual attempt's completed correlated helper refusal.
func abortSettledHostAttemptNewV1(ctx context.Context, r *GrantRequest, plan *newSecretPlan) (handled, retired bool, failure error) {
	if r == nil {
		return false, false, nil
	}
	r.mu.Lock()
	attempt := r.hostAttempt
	r.mu.Unlock()
	if attempt == nil {
		return false, false, nil
	}
	attempt.mu.Lock()
	var proof *settledNewRefusalV1
	if attempt.failure != nil {
		proof = attempt.failure.settledRefusal
	}
	attempt.mu.Unlock()
	if proof == nil {
		return false, false, nil
	}
	if proof.owner == nil || proof.owner.attempt != attempt || proof.owner.request != r || proof.owner.plan != plan {
		return true, false, grantOperationError()
	}
	if err := proof.checkOriginalV1(ctx); err != nil {
		return true, false, err
	}
	if plan == nil {
		// A start without a prospective local row still needs the actual
		// remote terminal result and blocked future attempt before cleanup.
		attempt.mu.Lock()
		valid := attempt.closing && !attempt.started && attempt.running == nil
		attempt.mu.Unlock()
		return true, valid, nil
	}
	plan.mu.Lock()
	if plan.retired {
		valid := plan.aborting && plan.attempted && !plan.published
		plan.mu.Unlock()
		return true, valid, nil
	}
	original := cloneSandboxPolicy(plan.creation)
	plan.mu.Unlock()
	if original == nil {
		return true, false, sandboxReceiptError()
	}
	retired, failure = retireSettledNewSandboxContextV1(ctx, proof.owner.conn, original, proof)
	if retired {
		plan.mu.Lock()
		plan.retired, plan.aborting = true, true
		plan.mu.Unlock()
	}
	return true, retired, failure
}

func (proof *settledNewRefusalV1) checkOriginalV1(ctx context.Context) error {
	if ctx == nil || proof == nil || proof.owner == nil || proof.settlement.Validate() != nil {
		return grantOperationError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner := proof.owner
	owner.mu.Lock()
	valid := owner.prepared && owner.sent && owner.conn != nil && owner.request != nil && owner.attempt != nil && owner.attempt.plan != nil && owner.original != nil &&
		owner.requestID == proof.settlement.RequestID && owner.member == proof.settlement.SessionID &&
		owner.nonce == proof.settlement.Attempt && owner.conn.HostID() == proof.settlement.HostID
	source := owner.source
	owner.mu.Unlock()
	if !valid {
		return sandboxReceiptError()
	}
	owner.request.mu.Lock()
	valid = owner.request.hostAttempt == owner.attempt && owner.request.newSecrets == owner.plan &&
		owner.request.purpose == grantRemote && owner.request.operation == grantOpNew &&
		owner.request.operationUntrusted == owner.request.Untrusted &&
		owner.request.Untrusted == owner.attempt.plan.need.Untrusted && owner.request.SSHAgent == owner.attempt.plan.args.SSHAgent
	owner.request.mu.Unlock()
	owner.attempt.mu.Lock()
	valid = valid && owner.attempt.request == owner.request && owner.attempt.conn == owner.conn &&
		owner.attempt.failure != nil && owner.attempt.failure.settledRefusal == proof && !owner.attempt.started
	owner.attempt.mu.Unlock()
	if !valid {
		return sandboxReceiptError()
	}
	bounded, end, err := narrowHostAttemptContextV1(context.WithoutCancel(owner.original), ctx)
	if err != nil {
		return err
	}
	defer end()
	return withSandboxReceiptObservation(bounded, owner.conn, func(owned context.Context) error { return source.check(owned, owner.conn) })
}

// The concrete terminal lease never clears attempted. It permits only the
// unchanged original prospective row's protected transaction after a genuine
// helper settlement. The old never-attempted proof contract stays separate.
func (proof *settledNewRefusalV1) holdSettledNewSandboxRetirementV1(ctx context.Context, c *Conn, original *SandboxPolicy) (func(), error) {
	if err := proof.checkOriginalV1(ctx); err != nil {
		return nil, err
	}
	owner := proof.owner
	plan := owner.plan
	if plan == nil || c != owner.conn {
		return nil, sandboxReceiptError()
	}
	owner.attempt.mu.Lock()
	closing := owner.attempt.closing && owner.attempt.running == nil
	owner.attempt.mu.Unlock()
	if !closing {
		return nil, grantOperationError()
	}
	plan.mu.Lock()
	if plan.conn != c || !plan.attempted || plan.closed || plan.busy || plan.published || plan.retired ||
		!sameSandboxCreation(plan.creation, original) || original.SessionID != proof.settlement.SessionID || original.HostID != proof.settlement.HostID {
		plan.mu.Unlock()
		return nil, sandboxReceiptError()
	}
	plan.aborting = true
	return plan.mu.Unlock, nil
}

func retireSettledNewSandboxContextV1(ctx context.Context, c *Conn, original *SandboxPolicy, proof *settledNewRefusalV1) (bool, error) {
	if ctx == nil || proof == nil || c == nil || original == nil || original.HostID != c.HostID() {
		return false, sandboxReceiptError()
	}
	frozen := cloneSandboxPolicy(original)
	if err := validateUnpublishedSandboxSnapshot(frozen); err != nil {
		return false, err
	}
	release, err := proof.holdSettledNewSandboxRetirementV1(ctx, c, frozen)
	if err != nil {
		return false, err
	}
	defer release()
	// This shared lower transaction still performs the original source and
	// exact prospective row CAS. It accepts no callback or authority DTO.
	return retireProspectiveSandboxLockedV1(ctx, c, frozen)
}
