package client

import (
	"context"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

// agentSandboxPolicy deliberately cannot stand in for the full policy or
// its opaque sources. Its only consumer is explicit agent admission.
type agentSandboxPolicy struct {
	FutureLineage  bool
	FutureDelivery bool
	HostID         string
	Ceiling        Tier
	SSHAgent       bool
	CreationWall   string
}

func readAgentSandboxPolicy(ctx context.Context, h Host, id string) (*agentSandboxPolicy, error) {
	if err := checkSessionID(h.Name, id); err != nil {
		return nil, err
	}
	if !ValidHostName(h.Name) {
		return nil, badHostNameError(h.Name)
	}
	var found *agentSandboxPolicy
	err := withAgentPolicySnapshot(ctx, h, func(source io.ReaderAt, size int64, exists bool) error {
		if !exists {
			return nil
		}
		var err error
		found, err = projectAgentSandboxPolicy(ctx, source, size, h.Name, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func projectAgentSandboxPolicy(ctx context.Context, source io.ReaderAt, size int64, host, id string) (*agentSandboxPolicy, error) {
	p, err := api.NewJSONProjection(ctx, source, size)
	if err != nil {
		return nil, agentProjectionError(ctx, err)
	}
	root, err := p.ObjectRoot()
	if err != nil {
		return nil, agentProjectionError(ctx, err)
	}
	var fields [3]api.JSONSpan
	err = p.Object(root, []string{"schema", "host", "sessions"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	})
	if err != nil {
		return nil, agentProjectionError(ctx, err)
	}
	schema := 0
	for _, version := range []string{"1", "2", "3", "4"} {
		yes, err := p.Literal(fields[0], version)
		if err != nil {
			return nil, agentProjectionError(ctx, err)
		}
		if yes {
			schema = int(version[0] - '0')
		}
	}
	if schema == 0 {
		return nil, badSandboxState()
	}
	yes, err := p.StringEqual(fields[1], host)
	if err != nil || !yes {
		return nil, agentProjectionError(ctx, err)
	}
	var selected *agentSandboxPolicy
	err = p.Object(fields[2], nil, func(field api.JSONField) error {
		if err := projectedSessionID(p, field.Name); err != nil {
			return err
		}
		wanted, err := p.StringEqual(field.Name, id)
		if err != nil {
			return err
		}
		facts, err := projectAgentRow(ctx, p, field.Value, field.Name, schema, wanted)
		if err != nil {
			return err
		}
		if wanted {
			copy := facts
			selected = &copy
		}
		return nil
	})
	if err != nil {
		return nil, agentProjectionError(ctx, err)
	}
	return selected, nil
}

func agentProjectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return badSandboxState()
}

// effectiveAgentSessionTier reads only the facts needed by explicit agent
// admission. Publication callers retain their fresh HostLock ownership.
func effectiveAgentSessionTier(ctx context.Context, h Host, id, hostID string) (Tier, *agentSandboxPolicy, error) {
	tier, savedID, err := grantHostTier(ctx, h)
	if err != nil {
		return TierUnknown, nil, err
	}
	if savedID != "" && savedID != hostID {
		return TierUnknown, nil, grantIdentityError(h)
	}
	policy, err := readAgentSandboxPolicy(ctx, h, id)
	if err != nil {
		return TierUnknown, nil, err
	}
	if policy != nil {
		if policy.HostID != "" && policy.HostID != hostID {
			return TierUnknown, nil, grantIdentityError(h)
		}
		tier = min(tier, policy.Ceiling)
	}
	return min(tier, requestCeiling(ctx, tier)), policy, nil
}
