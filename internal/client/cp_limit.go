package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
)

func copyPolicyUnknown() error {
	return &UserError{Code: "cp_policy", Msg: "the original local copy ceiling is unavailable or changed", Hint: "Use an original local copy approval from the current client; an older saved record cannot invent this approval.", Kind: KindRefused}
}

// copyLimit consumes the SAME original approved resource pin. Current remote
// policy can only narrow it. It performs no laptop file read, pending clear,
// settings reload, value command or receipt renewal.
func (c *Conn) copyLimit(ctx context.Context, id string, current *api.Policy, operation *settingsOperation) (int64, error) {
	if operation == nil {
		return 0, copyPolicyUnknown()
	}
	if err := operation.Revalidate(ctx); err != nil {
		return 0, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return 0, err
	}
	p, err := getSandboxReceiptPolicyContext(ctx, c, id)
	if err != nil {
		return 0, err
	}
	if p == nil || p.FutureLineage || len(p.PolicySources) == 0 {
		return 0, copyPolicyUnknown()
	}
	pin, err := api.DecodePolicySourcePin(p.PolicySources)
	if err != nil || pin == nil || pin.Resources == nil || pin.Resources.Version != 1 || pin.Resources.CPMax == nil {
		return 0, copyPolicyUnknown()
	}
	if current == nil || current.Preset == "" {
		return 0, copyPolicyUnknown()
	}
	if _, err := api.ParsePreset(string(current.Preset)); err != nil {
		return 0, copyPolicyUnknown()
	}
	limit, err := filecopy.CopyMax(current)
	if err != nil {
		return 0, err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return 0, err
	}
	return min(*pin.Resources.CPMax, limit), ctx.Err()
}
