package client

import "context"

// NewPushGrantRequest owns one local forge query and exact push mutation.
// Its answers cannot authorise a session, ordinary network grant or review.
func NewPushGrantRequest() *GrantRequest { return &GrantRequest{purpose: grantLocalForge} }

// NewRootStepGrantRequest owns one optional root-step plan and attempt. It
// cannot select secrets, create a session or donate answers to forge queries.
func NewRootStepGrantRequest() *GrantRequest { return &GrantRequest{purpose: grantLocalRoot} }

func requireLocalOperation(ctx context.Context, purpose grantPurpose, operation grantOperation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if r == nil || purpose != grantLocalForge && purpose != grantLocalRoot {
		return grantPurposeError()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return err
	}
	if r.purpose != purpose || r.SSHAgent || r.operation != grantOpUnset && r.operation != operation ||
		r.operation != grantOpUnset && r.operationUntrusted != r.Untrusted {
		return grantPurposeError()
	}
	r.operation, r.operationUntrusted = operation, r.Untrusted
	return nil
}

func (c *Conn) beginLocalGrant(ctx context.Context, purpose grantPurpose, operation grantOperation, kind string) (context.Context, func(), error) {
	if err := requireLocalOperation(ctx, purpose, operation); err != nil {
		return ctx, nil, err
	}
	return c.beginGrantSource(ctx, kind, true)
}

type forgeValuePurposeKey struct{}

func requirePurposeOutbound(ctx context.Context, r *GrantRequest, outbound Outbound) error {
	switch requestGrantPurpose(r) {
	case grantRemote:
		return requireRemoteGrantRequest(ctx)
	case grantLocalReview:
		if outbound.Kind == OutboundSecret {
			return nil
		}
	case grantLocalForge:
		if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
			return err
		}
		if outbound.Kind == OutboundPush || outbound.Kind == OutboundPublicPush {
			return requirePushOutbound(ctx, r, outbound)
		}
		plan, _ := ctx.Value(forgeValuePurposeKey{}).(*localForgePlan)
		if outbound.Kind == OutboundSecret && plan != nil && plan.request == r && plan.allowsName(outbound.Name) {
			return nil
		}
	case grantLocalRoot:
		if outbound.Kind == OutboundRootRun {
			return requireRootOutbound(ctx, r, outbound)
		}
	}
	return grantPurposeError()
}

func requirePurposeRecord(ctx context.Context, r *GrantRequest, outbound Outbound) error {
	if r == nil {
		return requireRemoteGrantRequest(ctx)
	}
	switch requestGrantPurpose(r) {
	case grantRemote:
		return requireRemoteGrantRequest(ctx)
	case grantLocalForge:
		if outbound.Kind == OutboundPush || outbound.Kind == OutboundPublicPush {
			return requirePushOutbound(ctx, r, outbound)
		}
	case grantLocalRoot:
		if outbound.Kind == OutboundRootRun {
			return requireRootOutbound(ctx, r, outbound)
		}
	}
	return grantPurposeError()
}
