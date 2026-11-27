package client

import (
	"context"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// agentSandboxPolicy deliberately cannot stand in for the full policy or
// its opaque sources. Its only consumer is explicit agent admission.
type agentSandboxPolicy struct {
	FutureLineage  bool
	FutureDelivery bool
	FutureReceipt  bool
	FutureInput    bool
	HostID         string
	Ceiling        Tier
	SSHAgent       bool
	CreationWall   string
}

func readAgentSandboxPolicy(ctx context.Context, h Host, id string) (found *agentSandboxPolicy, returned error) {
	mode, err := newAgentModeResources(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, mode.closeInactive()) }()
	return readAgentSandboxPolicyWithMode(ctx, h, id, mode)
}

// Both preliminary attach facts and later live grants borrow the actual
// agent mode's ONE backing/slab. No raw dense map pass precedes that scope.
func readAgentSandboxPolicyWithMode(ctx context.Context, h Host, id string, mode *agentModeResources) (found *agentSandboxPolicy, returned error) {
	if err := checkSessionID(h.Name, id); err != nil {
		return nil, err
	}
	if !ValidHostName(h.Name) {
		return nil, badHostNameError(h.Name)
	}
	if mode == nil || mode.inspector == nil || mode.inspector.engine == nil {
		return nil, agentModeResourceError()
	}
	snapshot, err := openAgentSettingsSnapshot(ctx)
	if err != nil {
		return nil, agentSettingsFailure(err)
	}
	defer func() { returned = errors.Join(returned, snapshot.close()) }()
	err = withHostStateReadContext(ctx, h, func(owned context.Context) error {
		return withStateTransaction(owned, []string{"hosts", HostFileKey(h.Name)}, "sandboxes.json", false, false, func(tx *stateFileTxn) error {
			if !tx.view().Exists {
				return nil
			}
			scope, err := newAgentMetadataScope(mode, snapshot, tx)
			if err != nil {
				return err
			}
			defer scope.close()
			err = mode.inspector.engine.WithSources(scope.ctx, scope.descriptors(), func(inspection *tomlinspect.Inspection) error {
				defer scope.finishCachedWork()
				if err := inspection.InspectTOML(0, &mode.inspector.visitor); err != nil {
					return err
				}
				var err error
				found, err = inspectAgentSandboxPolicies(inspection, h.Name, id)
				return err
			})
			if err != nil {
				return agentSettingsFailure(err)
			}
			return scope.check(scope.ctx)
		})
	})
	return found, err
}

func projectAgentSandboxPolicy(ctx context.Context, source io.ReaderAt, size int64, host, id string) (*agentSandboxPolicy, error) {
	p, err := api.NewJSONProjection(ctx, source, size)
	if err != nil {
		return nil, agentProjectionError(ctx, err)
	}
	return projectAgentSandboxPolicyProjection(ctx, p, host, id, nil)
}

// The combined inspector supplies its ONE source-bound projection and visits
// receipt metadata for every row before any selected facts can be used.
func projectAgentSandboxPolicyProjection(ctx context.Context, p jsonValueProjection, host, id string, visit func(api.JSONSpan, api.JSONSpan, int, *agentSandboxPolicy) error) (*agentSandboxPolicy, error) {
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
		if visit != nil {
			if err := visit(field.Value, field.Name, schema, &facts); err != nil {
				return err
			}
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
func effectiveAgentSessionTier(ctx context.Context, h Host, id, hostID string, mode *agentModeResources) (Tier, *agentSandboxPolicy, error) {
	tier, savedID, err := grantHostTier(ctx, h)
	if err != nil {
		return TierUnknown, nil, err
	}
	if savedID != "" && savedID != hostID {
		return TierUnknown, nil, grantIdentityError(h)
	}
	policy, err := readAgentSandboxPolicyWithMode(ctx, h, id, mode)
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
