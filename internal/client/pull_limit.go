package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// DefaultPullMax is the specified fallback when this laptop has no valid
// approved creation-layer projection for the selected original session.
const DefaultPullMax = api.PullMaxDefault

// LocallyApprovedPullMax reads only original saved local creation data. It
// never pivots from a host row, refreshes repository policy or parses config.
// The narrowed source pin preserves original session overrides; absent old
// effective top/host/profile values are unavailable, never guessed.
func LocallyApprovedPullMax(ctx context.Context, h Host, originalID string) (int64, error) {
	local, err := LoadSandboxPolicy(ctx, h, originalID)
	if err != nil {
		return 0, err
	}
	if local == nil || local.FutureLineage || len(local.PolicySources) == 0 {
		return DefaultPullMax, nil
	}
	pin, err := api.DecodePolicySourcePin(local.PolicySources)
	if err != nil {
		return 0, badPolicySources()
	}
	return pullMaxFromPin(pin)
}

func pullMaxFromPin(pin *api.PolicySourcePin) (int64, error) {
	if pin == nil {
		return DefaultPullMax, nil
	}
	if pin.Resources != nil {
		return pin.Resources.PullMax, nil // the one pin decoder validated it
	}
	if pin.Session.Sandbox.PullMax == nil {
		return DefaultPullMax, nil
	}
	limit, err := api.ParseSize(*pin.Session.Sandbox.PullMax)
	if err != nil || limit <= 0 {
		return 0, &UserError{Msg: "the original local pull limit is invalid", Hint: "Use a valid approved creation setting or the SDK's explicit positive limit.", Code: "bad_config", Kind: KindUsage}
	}
	return limit, nil
}

func badPolicySources() error {
	return &UserError{Msg: "the saved policy source pin is malformed or incompatible", Hint: "Use the original laptop's session policy, or start a new session.", Code: "policy_sources", Kind: KindRefused}
}
