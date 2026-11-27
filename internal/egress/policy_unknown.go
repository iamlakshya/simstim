package egress

import "github.com/iamlakshya/simstim/internal/api"

// Preserve actual scalar presence. Empty here is the legacy omitted setting,
// not an inferred deny/allow permission. Canonical layer output supplies its
// real builtin ask value, and structured presence uses the same validator.
func configuredUnknown(policy api.Policy) (api.Unknown, error) {
	if policy.Unknown == "" && policy.Network.Unknown == nil {
		return "", nil
	}
	legacy := policy.Unknown
	if legacy != "" {
		parsed, err := api.ParseUnknown(string(legacy))
		if err != nil {
			return "", err
		}
		legacy = parsed
	}
	layer := api.Layer{Unknown: legacy, LayerValues: api.LayerValues{Network: api.NetworkValues{Unknown: policy.Network.Unknown}}}
	if err := canonicalLayerValues(&layer); err != nil {
		return "", err
	}
	return layer.Unknown, nil
}
