package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// The live agent grant keeps both original sources inside one state
// transaction. Only the actual mode owner can admit the combined lease.
func (i *agentSettingsInspector) allowSession(ctx context.Context, c *Conn, session string) (err error) {
	if i == nil || i.engine == nil || ctx == nil || c == nil {
		return validatedConfigScopeError()
	}
	mode, ok := i.mode.(*agentModeResources)
	if !ok || mode == nil || mode.inspector != i {
		return agentModeResourceError()
	}
	if err := checkSessionID(c.Host.Name, session); err != nil {
		return err
	}
	if err := mode.checkAgentSettingsMode(ctx); err != nil {
		return err
	}
	tier, savedID, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return err
	}
	if savedID != "" && savedID != c.HostID() {
		return grantIdentityError(c.Host)
	}
	snapshot, err := openAgentSettingsSnapshot(ctx)
	if err != nil {
		return agentSettingsFailure(err)
	}
	defer func() {
		if failure := snapshot.close(); failure != nil {
			err = errors.Join(err, agentSettingsFailure(failure))
		}
	}()
	return withHostStateReadContext(ctx, c.Host, func(owned context.Context) error {
		return withStateTransaction(owned, []string{"hosts", HostFileKey(c.Host.Name)}, "sandboxes.json", false, false, func(tx *stateFileTxn) error {
			if !tx.view().Exists {
				return grantIdentityError(c.Host)
			}
			return i.allowSessionTransaction(mode, snapshot, tx, c, session, tier)
		})
	})
}

func (i *agentSettingsInspector) allowSessionTransaction(mode *agentModeResources, snapshot *agentSettingsSnapshot, tx *stateFileTxn, c *Conn, session string, tier Tier) error {
	scope, err := newAgentMetadataScope(mode, snapshot, tx)
	if err != nil {
		return agentSettingsFailure(err)
	}
	defer scope.close()
	var selected *agentSandboxPolicy
	err = i.engine.WithSources(scope.ctx, scope.descriptors(), func(h *tomlinspect.Inspection) error {
		// Cached source work has ended before the inspector retires regions,
		// checks the final sources and closes the ONE physical index lease.
		defer scope.finishCachedWork()
		if err := h.InspectTOML(0, &i.visitor); err != nil {
			return agentSettingsFailure(err)
		}
		var err error
		selected, err = inspectAgentSandboxPolicies(h, c.Host.Name, session)
		return err
	})
	if err != nil {
		return agentSettingsFailure(err)
	}
	// Neither the inspection nor any region escapes. These final checks
	// still borrow the original settings FD and guarded transaction.
	if err := scope.check(scope.ctx); err != nil {
		return agentSettingsFailure(err)
	}
	if selected == nil || selected.HostID == "" || selected.HostID != c.HostID() ||
		!selected.SSHAgent || selected.CreationWall != "none" {
		return grantIdentityError(c.Host)
	}
	if selected.FutureLineage || selected.FutureDelivery {
		return futureDeliveryError()
	}
	if selected.FutureReceipt || selected.FutureInput {
		return futureSandboxReceiptError()
	}
	if !c.HasFeature(agentbridge.Feature) {
		return c.grantHelperOld("share an agent with one session")
	}
	tier = min(tier, selected.Ceiling)
	tier = min(tier, requestCeiling(scope.ctx, tier))
	barrier := agentSettingsBarrier{ctx: scope.ctx, mode: mode, snapshot: snapshot, scope: scope, validated: true}
	defer func() { barrier.validated = false }()
	return allowWithValidatedConfig(c.Host, tier, Outbound{Kind: OutboundSSHAgent}, nil, nil, &barrier)
}
