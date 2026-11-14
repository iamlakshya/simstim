// Package secretmode defines the shared credential delivery mode without any
// dependency on API, protocol, configuration or runtime packages.
package secretmode

// SecretMode records bound delivery or explicit exposure intent.
type SecretMode string

const (
	SecretModeBound   SecretMode = "bound"
	SecretModeExposed SecretMode = "exposed"
)

// Valid reports whether the mode is one explicit supported publication mode.
func (mode SecretMode) Valid() bool {
	return mode == SecretModeBound || mode == SecretModeExposed
}
