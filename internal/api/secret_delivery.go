package api

import "maps"

const SecretDeliveryIntentV1 = 1

// SecretDeliveryIntent is locally derived metadata, not permission to fetch
// or publish a value. Its name domain is the original local allowed set.
// Public sentinels, shapes and server state never provide that authority.
type SecretDeliveryIntent struct {
	Version int                   `json:"version"`
	Modes   map[string]SecretMode `json:"modes"`
	Binds   map[string]SecretBind `json:"binds"`
}

// CloneSecretDeliveryIntent owns all mutable metadata before request sealing.
func CloneSecretDeliveryIntent(input SecretDeliveryIntent) SecretDeliveryIntent {
	input.Modes = maps.Clone(input.Modes)
	input.Binds = CloneSecretBinds(input.Binds)
	return input
}
