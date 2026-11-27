package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

type namedSSHPolicySourceV1 struct {
	request   *GrantRequest
	conn      *Conn
	stamp     *hostTransportStamp
	operation *settingsOperation
	id        string
	member    *SandboxPolicy
	creation  *SandboxPolicy
	ceiling   Tier
}

// NewNamedSSHKeyGrantRequest owns one names-only local policy action. It
// cannot run a key command or donate its answers to a resume or a new start.
func NewNamedSSHKeyGrantRequest() *GrantRequest {
	return &GrantRequest{purpose: grantRemote, operation: grantOpNamedSSHPolicy}
}

func retainedNamedSSHPolicySourceV1(ctx context.Context, c *Conn, id string) (*settingsOperation, *SandboxPolicy, Tier, error) {
	r := grantRequestOf(ctx)
	if r == nil || c == nil || !api.ValidSessionID(id) {
		return nil, nil, TierUnknown, namedSSHPolicySourceError()
	}
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return nil, nil, TierUnknown, err
	}
	r.mu.Lock()
	op, original := r.operation, r.namedPolicySource
	r.mu.Unlock()
	if op != grantOpResume && op != grantOpNamedSSHPolicy {
		return nil, nil, TierUnknown, grantOperationError()
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, nil, TierUnknown, err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return nil, nil, TierUnknown, err
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return nil, nil, TierUnknown, err
	}
	tier, member, err := EffectiveSessionTier(ctx, c.Host, id, c.HostID())
	if err != nil {
		return nil, nil, TierUnknown, err
	}
	if member == nil || member.HostID != c.HostID() || member.FutureLineage || member.FutureDelivery || member.futureDeliveryReceipt || member.futureInputOperation || member.CreationWall == "none" {
		return nil, nil, TierUnknown, namedSSHPolicySourceError()
	}
	creation := member
	if primary := localSandboxRoot(member); primary != id {
		creation, err = LoadSandboxPolicy(ctx, c.Host, primary)
		if err != nil {
			return nil, nil, TierUnknown, err
		}
		if creation == nil || creation.HostID != c.HostID() || creation.FutureLineage || creation.FutureDelivery || creation.futureDeliveryReceipt || creation.futureInputOperation {
			return nil, nil, TierUnknown, namedSSHPolicySourceError()
		}
	}
	if _, err := decodeNamedSSHKeyChoicesV1(creation.sshKeyChoicesRaw); err != nil {
		return nil, nil, TierUnknown, err
	}
	tier = min(tier, member.Ceiling, creation.Ceiling, requestCeiling(ctx, tier))
	if original == nil {
		candidate := &namedSSHPolicySourceV1{request: r, conn: c, stamp: c.hostTransport, operation: operation, id: id,
			member: cloneSandboxPolicy(member), creation: cloneSandboxPolicy(creation), ceiling: tier}
		r.mu.Lock()
		if err := r.lifetimeErrorLocked(); err != nil {
			r.mu.Unlock()
			return nil, nil, TierUnknown, err
		}
		if r.namedPolicySource == nil {
			r.namedPolicySource = candidate
		}
		original = r.namedPolicySource
		r.mu.Unlock()
	}
	if original.request != r || original.conn != c || original.stamp != c.hostTransport || original.operation != operation || original.id != id ||
		!sameSandboxCreation(original.member, member) || !sameSandboxCreation(original.creation, creation) {
		return nil, nil, TierUnknown, namedSSHPolicySourceError()
	}
	r.mu.Lock()
	original.ceiling = min(tier, original.ceiling)
	tier = original.ceiling
	r.mu.Unlock()
	if err := operation.Revalidate(ctx); err != nil {
		return nil, nil, TierUnknown, err
	}
	return operation, cloneSandboxPolicy(creation), tier, nil
}
