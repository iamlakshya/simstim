package api

import (
	"fmt"
	"io"
	"slices"
)

// RetiredPrimarySecretState preserves an old public stand-in for tripwires.
// Its historical binding validates metadata and grants no current route.
// It is never request-eligible, armable, fetched or reclassified as minted.
type RetiredPrimarySecretState struct {
	Sentinel Secret       `json:"sentinel"`
	Shape    *SecretShape `json:"shape"`
	Identity string       `json:"identity"`
	Binding  SecretBind   `json:"binding"`
}

// CloneRetiredPrimaryStates owns historical public metadata before saving.
func CloneRetiredPrimaryStates(input map[string][]RetiredPrimarySecretState) map[string][]RetiredPrimarySecretState {
	if input == nil {
		return nil
	}
	output := make(map[string][]RetiredPrimarySecretState, len(input))
	for name, entries := range input {
		owned := slices.Clone(entries)
		for i := range owned {
			owned[i].Binding.Hosts = slices.Clone(owned[i].Binding.Hosts)
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

func (RetiredPrimarySecretState) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }
