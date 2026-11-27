package api

import (
	"encoding/json"
	"maps"
)

// SessionSecretSummaryV1 identifies the compact, display-only session view.
const SessionSecretSummaryV1 = 1

// SessionSecretSummary describes applied delivery without public stand-ins,
// bindings, shapes or history. It never grants a name, a fetch or an arm.
// An absent, unsupported or incomplete view leaves delivery unknown.
type SessionSecretSummary struct {
	Version     int                     `json:"version"`
	Generation  string                  `json:"generation,omitempty"`
	Unsupported bool                    `json:"unsupported,omitempty"`
	Secrets     map[string]SecretStatus `json:"secrets,omitempty"`
}

// SecretStatus is one member's applied mode and current arming status.
type SecretStatus struct {
	Mode  SecretMode `json:"mode"`
	Armed bool       `json:"armed"`
}

// Supported reports whether this display view has a known, complete format.
func (s *SessionSecretSummary) Supported() bool {
	if s == nil || s.Version != SessionSecretSummaryV1 || s.Unsupported {
		return false
	}
	for _, status := range s.Secrets {
		if !status.Mode.Valid() {
			return false
		}
	}
	return true
}

// UnmarshalJSON keeps future or malformed summaries explicitly unknown.
func (s *SessionSecretSummary) UnmarshalJSON(raw []byte) error {
	type wire SessionSecretSummary
	var value wire
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	parsed := SessionSecretSummary(value)
	if !ExactJSONFields(raw, (*wire)(nil)) || !parsed.Supported() {
		*s = SessionSecretSummary{Version: value.Version, Unsupported: true}
		return nil
	}
	*s = parsed
	return nil
}

// CloneSessionSecretSummary owns the compact display map.
func CloneSessionSecretSummary(input *SessionSecretSummary) *SessionSecretSummary {
	if input == nil {
		return nil
	}
	output := *input
	output.Secrets = maps.Clone(input.Secrets)
	return &output
}

// SummariseSessionSecrets projects only selected member names from an already
// canonically validated public state. Validation supplies no fetch authority.
func SummariseSessionSecrets(state *SessionSecretState, names []string) *SessionSecretSummary {
	if state == nil {
		return nil
	}
	unknown := &SessionSecretSummary{Version: SessionSecretSummaryV1, Unsupported: true}
	if !state.Supported() || state.Generation == "" {
		return unknown
	}
	result := &SessionSecretSummary{Version: SessionSecretSummaryV1, Generation: state.Generation,
		Secrets: make(map[string]SecretStatus, len(names))}
	for _, name := range names {
		mode, present := state.Intent.Modes[name]
		public, applied := state.Primary[name]
		if !present || !applied || !mode.Valid() || public.Mode != mode {
			return unknown
		}
		result.Secrets[name] = SecretStatus{Mode: mode, Armed: public.Armed}
	}
	return result
}
