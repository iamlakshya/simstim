package api

import "maps"

const SecretDeliveryIntentV1 = 1

// FeatureJudgeKeyV1 is advertised only by a fully wired trusted judge route.
const FeatureJudgeKeyV1 = "judge-key-v1"

// TrustedJudgeUseV1 is the sole closed trusted credential use role.
const TrustedJudgeUseV1 = "trusted-judge-v1"

// TrustedSecretUseV1 is original local intent, never a fetch or guest grant.
// The protected value travels separately through the original operation.
// Mode keeps the existing bound delivery enum; it creates no new secret mode.
type TrustedSecretUseV1 struct {
	Version  int        `json:"v"`
	Use      string     `json:"use"`
	Mode     SecretMode `json:"mode"`
	Bind     SecretBind `json:"bind"`
	Model    string     `json:"model"`
	Endpoint string     `json:"endpoint"`
}

// CloneTrustedSecretUses owns binding slices before sealing an operation.
func CloneTrustedSecretUses(input map[string]TrustedSecretUseV1) map[string]TrustedSecretUseV1 {
	if input == nil {
		return nil
	}
	output := make(map[string]TrustedSecretUseV1, len(input))
	for name, use := range input {
		use.Bind = CloneSecretBinds(map[string]SecretBind{name: use.Bind})[name]
		output[name] = use
	}
	return output
}

// SecretDeliveryIntent is locally derived metadata, not permission to fetch
// or publish a value. Its name domain is the original local allowed set.
// Public sentinels, shapes and server state never provide that authority.
type SecretDeliveryIntent struct {
	Version     int                           `json:"version"`
	Modes       map[string]SecretMode         `json:"modes"`
	Binds       map[string]SecretBind         `json:"binds"`
	TrustedUses map[string]TrustedSecretUseV1 `json:"trusted_uses,omitempty"`
}

// CloneSecretDeliveryIntent owns all mutable metadata before request sealing.
func CloneSecretDeliveryIntent(input SecretDeliveryIntent) SecretDeliveryIntent {
	input.Modes = maps.Clone(input.Modes)
	input.Binds = CloneSecretBinds(input.Binds)
	input.TrustedUses = CloneTrustedSecretUses(input.TrustedUses)
	return input
}
