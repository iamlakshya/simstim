package client

import "context"

// This phase exists only inside the actual fixed host-prepare plan. The plan
// owns its mutex and original saved Source; no metadata can open another phase.
func sandboxPrepareImagePhaseV1(ctx context.Context, c *Conn) (*settingsOperation, error) {
	r := grantRequestOf(ctx)
	if r == nil || requestGrantPurpose(r) != grantHostPrepare || requestGrantOperation(r) != grantOpSandboxPrepare {
		return nil, grantPurposeError()
	}
	r.mu.Lock()
	plan := r.sandboxPrepare
	r.mu.Unlock()
	if plan == nil {
		return nil, grantOperationError()
	}
	return plan.imagePhaseV1(ctx, c)
}

func requireImageFetchFamilyV1(ctx context.Context, c *Conn, family grantOperation) error {
	if family == grantOpSandboxPrepare {
		_, err := sandboxPrepareImagePhaseV1(ctx, c)
		return err
	}
	if family != grantOpNew {
		return grantOperationError()
	}
	return requireGrantOperation(ctx, grantOpNew)
}

func captureSandboxPrepareImagePolicyV1(ctx context.Context, c *Conn) (context.Context, error) {
	operation, err := sandboxPrepareImagePhaseV1(ctx, c)
	if err != nil {
		return ctx, err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return ctx, err
	}
	r := grantRequestOf(ctx)
	candidate := &imagePolicyUseV1{request: r, family: grantOpSandboxPrepare, conn: c, stamp: c.hostTransport, source: c.hostTransport.source, hostID: c.HostID(), operation: operation}
	// The fixed release image never inherits sandbox.image or an OCI override.
	// Settings still own the original native credential/config lookup namespace.
	if err := candidate.check(ctx, c); err != nil {
		return ctx, err
	}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return ctx, err
	}
	before := r.imagePolicy
	if before == nil {
		r.imagePolicy = candidate
		before = candidate
	}
	r.mu.Unlock()
	if before.family != grantOpSandboxPrepare || before.operation != operation || before.conn != c || before.stamp != candidate.stamp || before.policy != nil {
		return ctx, grantOperationError()
	}
	if err := before.check(ctx, c); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, imagePolicyUseKeyV1{}, before), nil
}
