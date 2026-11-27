package supervisor

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func requireTrustedJudgeControl(ctx context.Context, owner *egressProc, uses map[string]api.TrustedSecretUseV1) error {
	if len(uses) == 0 {
		return nil
	}
	if owner == nil || owner.ctl == nil || owner.dead.Load() {
		return secretIntentFailure()
	}
	var raw json.RawMessage
	if err := owner.ctl.request(ctx, egress.CtlCapabilities, nil, &raw); err != nil {
		return err
	}
	var capabilities egress.CapabilitiesResult
	if !api.ExactJSONFields(raw, capabilities) || json.Unmarshal(raw, &capabilities) != nil || !slices.Contains(capabilities.Features, api.FeatureJudgeKeyV1) {
		return &proto.Error{Code: "helper_old", Message: "the running egress lacks the trusted judge credential role", Hint: "Use matching helpers before passing a classifier credential. Saved metadata cannot arm a key."}
	}
	return nil
}

func validateProtectedTrustedValues(uses map[string]api.TrustedSecretUseV1, values map[string]api.Secret, complete bool) error {
	if complete && len(uses) != len(values) {
		return secretIntentFailure()
	}
	for name, use := range uses {
		if err := egress.ValidateTrustedSecretUseV1(name, use); err != nil {
			return err
		}
	}
	for name := range values {
		if _, present := uses[name]; !present {
			return secretIntentFailure()
		}
	}
	return nil
}

func overlayTrustedUses(previous, incoming map[string]api.TrustedSecretUseV1) map[string]api.TrustedSecretUseV1 {
	output := api.CloneTrustedSecretUses(previous)
	if output == nil && incoming != nil {
		output = make(map[string]api.TrustedSecretUseV1, len(incoming))
	}
	for name, use := range api.CloneTrustedSecretUses(incoming) {
		output[name] = use
	}
	return output
}

func memberTrustedValues(input []map[string]api.Secret) (map[string]api.Secret, error) {
	if len(input) > 1 {
		return nil, secretIntentFailure()
	}
	if len(input) == 0 {
		return nil, nil
	}
	return maps.Clone(input[0]), nil
}
