package supervisor

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func requirePublicOnlyEgress(ctx context.Context, owner *egressProc, policy api.Policy) error {
	if !api.PolicyUsesPublicOnly(policy) {
		return nil
	}
	failure := &proto.Error{Code: "helper_old", Message: "the live egress cannot preserve public-only network addresses", Hint: "Update matching helpers, then stop and resume before applying this restricted allowance."}
	if owner == nil || owner.ctl == nil || owner.dead.Load() || channelClosed(owner.ctl.done) {
		return failure
	}
	var raw json.RawMessage
	if err := owner.ctl.request(ctx, egress.CtlCapabilities, nil, &raw); err != nil {
		return err
	}
	var result egress.CapabilitiesResult
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil {
		owner.ctl.closeOwnedControl()
		return failure
	}
	if !slices.Contains(result.Features, api.FeatureNetworkPublicOnly) {
		return failure
	}
	return nil
}
