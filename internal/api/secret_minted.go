package api

import (
	"fmt"
	"io"
	"slices"
)

// PublicMintedSecretState contains only a public response stand-in. A cold
// restore never recovers its server-issued real value from a local command.
// The old stand-in remains permanently unarmed until a fresh origin mint.
type PublicMintedSecretState struct {
	Sentinel  Secret       `json:"sentinel"`
	Shape     *SecretShape `json:"shape"`
	Identity  string       `json:"identity"`
	Authority string       `json:"authority"`
	Rule      string       `json:"rule"`
}

func ClonePublicMintedStates(input map[string][]PublicMintedSecretState) map[string][]PublicMintedSecretState {
	if input == nil {
		return nil
	}
	output := make(map[string][]PublicMintedSecretState, len(input))
	for name, states := range input {
		owned := slices.Clone(states)
		for i := range owned {
			if owned[i].Shape != nil {
				shape := *owned[i].Shape
				shape.Slots = slices.Clone(shape.Slots)
				owned[i].Shape = &shape
			}
		}
		output[name] = owned
	}
	return output
}

func (PublicMintedSecretState) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }
