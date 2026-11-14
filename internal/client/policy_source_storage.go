package client

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func encodeLocalPolicySources(a NewArgs) (json.RawMessage, error) {
	// A legacy creation with no selected local layers gets no fabricated pin.
	if len(a.Layers) == 0 {
		return nil, nil
	}
	// StartPolicyLayers includes only the repository file already checked by
	// the canonical creation preparation. Pin only laptop-selected identities;
	// the file can narrow the ceiling without becoming a trusted local source.
	local, _, err := egress.MergeLayers(a.Layers)
	if err != nil {
		return nil, policySourceStateError()
	}
	policy, _, err := egress.MergeLayers(StartPolicyLayers(a))
	if err != nil {
		return nil, policySourceStateError()
	}
	localLimit, err := creationPullMax(local)
	if err != nil {
		return nil, err
	}
	checkedLimit, err := creationPullMax(policy)
	if err != nil {
		return nil, err
	}
	// A checked remote file may narrow an already approved resource ceiling;
	// ordinary scalar precedence must never make it raise local approval.
	limit := min(localLimit, checkedLimit)
	pin, err := api.PinPolicySourcesWithResources(a.Layers, api.PolicyResourcePin{Version: 1, PullMax: limit})
	if err != nil {
		return nil, policySourceStateError()
	}
	raw, err := json.Marshal(pin)
	if err != nil {
		return nil, policySourceStateError()
	}
	return raw, nil
}

func creationPullMax(policy api.Policy) (int64, error) {
	if policy.Sandbox.PullMax == nil {
		return api.PullMaxDefault, nil
	}
	limit, err := api.ParseSize(*policy.Sandbox.PullMax)
	if err != nil || limit <= 0 {
		return 0, policySourceStateError()
	}
	return limit, nil
}

func policySourceStateError() error {
	return &UserError{Msg: "the session's local policy source pin is invalid or changed on retry",
		Hint: "Start a new request after choosing its local policy layers. A host reply cannot choose them.", Code: "policy_sources", Kind: KindRefused}
}
