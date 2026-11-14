package api

import (
	"fmt"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/secretmode"
)

// SecretMode preserves the API spelling of the shared delivery enum.
// The dependency-free definition keeps protocol DTOs below this API package.
type SecretMode = secretmode.SecretMode

const (
	SecretModeBound   = secretmode.SecretModeBound
	SecretModeExposed = secretmode.SecretModeExposed
	SecretShapeV1     = 1
)

// SecretShape is public format metadata. Classes come from trusted compiler
// policy or a pinned finite grammar, never an observed private alphabet.
type SecretShape struct {
	Version int               `json:"version"`
	Prefix  string            `json:"prefix,omitempty"`
	Rule    string            `json:"rule,omitempty"`
	Slots   []SecretShapeSlot `json:"slots"`
}

// SecretShapeSlot is a run of one fixed canonical class and UTF8 width.
// ASCII is used only for a source-backed finite ASCII alphabet or literal.
type SecretShapeSlot struct {
	Class string `json:"class"`
	Count int    `json:"count"`
	Width int    `json:"width"`
	ASCII string `json:"ascii,omitempty"`
}

// PublicSecretState may be persisted. It has no real value or value history.
// Identity is server-owned metadata and cannot authorise a new binding/name.
type PublicSecretState struct {
	Mode     SecretMode   `json:"mode"`
	Sentinel Secret       `json:"sentinel,omitempty"`
	Shape    *SecretShape `json:"shape,omitempty"`
	Identity string       `json:"identity,omitempty"`
	Armed    bool         `json:"armed"`
}

// ClonePublicSecretStates owns every shape and slot array before publication.
func ClonePublicSecretStates(input map[string]PublicSecretState) map[string]PublicSecretState {
	if input == nil {
		return nil
	}
	output := make(map[string]PublicSecretState, len(input))
	for name, state := range input {
		if state.Shape != nil {
			shape := *state.Shape
			shape.Slots = slices.Clone(shape.Slots)
			state.Shape = &shape
		}
		output[name] = state
	}
	return output
}

func (SecretShape) Format(f fmt.State, _ rune)       { _, _ = io.WriteString(f, Redacted) }
func (SecretShapeSlot) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, Redacted) }
func (PublicSecretState) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }
