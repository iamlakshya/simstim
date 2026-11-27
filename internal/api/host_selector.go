package api

import (
	"encoding/json"
	"fmt"
	"slices"
)

// HostSelectorV1 is one scalar placement choice. An ordered list replaces a
// broader choice. Its names grant no host, sandbox or secret authority.
type HostSelectorV1 struct {
	Names []string
	Auto  bool
}

// Validate checks the complete choice without changing its original order.
func (s HostSelectorV1) Validate() error {
	if s.Auto && len(s.Names) == 0 {
		return nil
	}
	if s.Auto || len(s.Names) == 0 {
		return hostSelectorError()
	}
	seen := make(map[string]bool, len(s.Names))
	for _, name := range s.Names {
		var grammar HostNameValidationV1
		for _, r := range name {
			grammar.Rune(r)
		}
		if !grammar.Valid() || seen[name] {
			return hostSelectorError()
		}
		seen[name] = true
	}
	return nil
}

func hostSelectorError() error {
	return fmt.Errorf("sandbox.host needs auto, a host name or a non-empty ordered list of distinct host names")
}

// DecodeHostSelectorV1 checks the native union shape. Validate checks its
// meaning in the canonical layer validator, as for other policy scalars.
func DecodeHostSelectorV1(raw any) (HostSelectorV1, error) {
	var next HostSelectorV1
	switch value := raw.(type) {
	case string:
		if value == "auto" {
			next.Auto = true
		} else {
			next.Names = []string{value}
		}
	case []string:
		next.Names = slices.Clone(value)
	case []any:
		next.Names = make([]string, len(value))
		for i, item := range value {
			name, ok := item.(string)
			if !ok {
				return HostSelectorV1{}, hostSelectorError()
			}
			next.Names[i] = name
		}
	default:
		return HostSelectorV1{}, hostSelectorError()
	}
	return next, nil
}

// UnmarshalTOML publishes a complete string/list union without partial writes.
func (s *HostSelectorV1) UnmarshalTOML(raw any) error {
	if s == nil {
		return hostSelectorError()
	}
	next, err := DecodeHostSelectorV1(raw)
	if err == nil {
		*s = next
	}
	return err
}

// MarshalTOML retains a scalar value during a typed config round trip.
func (s HostSelectorV1) MarshalTOML() ([]byte, error) { return s.MarshalJSON() }

// MarshalJSON retains ordered alternatives as one scalar setting.
func (s HostSelectorV1) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Auto {
		return json.Marshal("auto")
	}
	if len(s.Names) == 1 {
		return json.Marshal(s.Names[0])
	}
	return json.Marshal(s.Names)
}

// UnmarshalJSON uses the same string/list union and rejects object encodings.
func (s *HostSelectorV1) UnmarshalJSON(raw []byte) error {
	if s == nil {
		return hostSelectorError()
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	next, err := DecodeHostSelectorV1(value)
	if err == nil {
		err = next.Validate()
	}
	if err == nil {
		*s = next
	}
	return err
}
