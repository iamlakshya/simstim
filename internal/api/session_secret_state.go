package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const SessionSecretStateV1 = 1

// SessionSecretState contains only applied intent and public stand-ins.
// It never grants a secret name, a fetch or a destination permission.
type SessionSecretState struct {
	Version    int                                    `json:"version"`
	Intent     SecretDeliveryIntent                   `json:"intent"`
	Generation string                                 `json:"generation"`
	Primary    map[string]PublicSecretState           `json:"primary"`
	Minted     map[string][]PublicMintedSecretState   `json:"minted,omitempty"`
	Retired    map[string][]RetiredPrimarySecretState `json:"retired,omitempty"`
	opaque     json.RawMessage
}

// Supported reports structural compatibility, not authorisation or validity.
// Callers still use the canonical binding, shape and original-name validators.
func (s SessionSecretState) Supported() bool {
	return s.Version == SessionSecretStateV1 && s.Intent.Version == SecretDeliveryIntentV1 && len(s.opaque) == 0
}

// UnmarshalJSON retains unsupported state without interpreting or losing it.
func (s *SessionSecretState) UnmarshalJSON(raw []byte) error {
	if !json.Valid(raw) {
		return errors.New("invalid public session secret state")
	}
	type wire SessionSecretState
	var parsed wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&parsed)
	if err != nil || parsed.Version != SessionSecretStateV1 || parsed.Intent.Version != SecretDeliveryIntentV1 || !ExactJSONFields(raw, (*SessionSecretState)(nil)) {
		*s = SessionSecretState{opaque: bytes.Clone(raw)}
		return nil
	}
	*s = SessionSecretState(parsed)
	return nil
}

// MarshalJSON preserves opaque future state. It never projects a legacy mode.
func (s SessionSecretState) MarshalJSON() ([]byte, error) {
	if len(s.opaque) != 0 {
		return bytes.Clone(s.opaque), nil
	}
	type wire SessionSecretState
	return json.Marshal(wire(s))
}

// CloneSessionSecretState owns all mutable public metadata and opaque bytes.
func CloneSessionSecretState(input *SessionSecretState) *SessionSecretState {
	if input == nil {
		return nil
	}
	output := *input
	output.Intent = CloneSecretDeliveryIntent(input.Intent)
	output.Primary = ClonePublicSecretStates(input.Primary)
	output.Minted = ClonePublicMintedStates(input.Minted)
	output.Retired = CloneRetiredPrimaryStates(input.Retired)
	output.opaque = bytes.Clone(input.opaque)
	return &output
}

func (SessionSecretState) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }
