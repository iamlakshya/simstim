package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// FeatureSandboxSnapshot refuses helpers that could ignore a complete CAS.
const FeatureSandboxSnapshot = "sandbox-snapshot-v1"

// SandboxSnapshot is structural host data, never local grant authority.
// Members contains the complete sorted direct child IDs, excluding Primary.
type SandboxSnapshot struct {
	Version    int      `json:"version"`
	Primary    string   `json:"primary"`
	Generation string   `json:"generation"`
	Members    []string `json:"members"`
}

// SandboxSnapshotArgs retains the original typed member selector.
type SandboxSnapshotArgs struct {
	// TrustedReadiness restricts one actual live private-registry observation.
	TrustedReadiness *TrustedSecretReadinessQueryV1 `json:"trusted_readiness,omitempty"`
	Cohort           bool                           `json:"cohort,omitempty"`
	ID               string                         `json:"id"`
}

// Clone owns the child list and preserves explicit empty-list presence.
func (s *SandboxSnapshot) Clone() *SandboxSnapshot {
	if s == nil {
		return nil
	}
	copy := *s
	if s.Members != nil {
		copy.Members = append([]string{}, s.Members...)
	}
	return &copy
}

// Check validates a complete declaration within the caller's existing frame
// bound. A selector must be the primary or an actual declared child.
func (s *SandboxSnapshot) Check(selector string, byteBudget int) error {
	if s == nil || s.Version != 1 || !ValidSessionID(s.Primary) || s.Members == nil {
		return fmt.Errorf("invalid sandbox snapshot version, primary or member list")
	}
	if len(s.Generation) != 64 {
		return fmt.Errorf("invalid sandbox generation digest")
	}
	decoded, err := hex.DecodeString(s.Generation)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != s.Generation {
		return fmt.Errorf("non-canonical sandbox generation digest")
	}
	if byteBudget <= 0 || len(s.Members) > byteBudget/4 {
		return fmt.Errorf("sandbox membership exceeds the existing frame bound")
	}
	included := selector == "" || selector == s.Primary
	previous := ""
	for _, id := range s.Members {
		if !ValidSessionID(id) || id == s.Primary || id <= previous {
			return fmt.Errorf("sandbox child IDs are invalid, repeated or unsorted")
		}
		previous = id
		if id == selector {
			included = true
		}
	}
	if !included {
		return fmt.Errorf("the original session is outside the declared sandbox")
	}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > byteBudget {
		return fmt.Errorf("sandbox snapshot exceeds the existing frame bound")
	}
	return nil
}

// DecodeSandboxSnapshot applies the one shared duplicate/alias barrier before
// interpreting the four v1 structural fields. It grants no local authority.
func DecodeSandboxSnapshot(raw []byte, selector string, byteBudget int) (*SandboxSnapshot, error) {
	if len(raw) > byteBudget {
		return nil, fmt.Errorf("sandbox snapshot exceeds the existing frame bound")
	}
	fields, err := JSONObjectFields(raw, "version", "primary", "generation", "members")
	if err != nil || len(fields) != 4 {
		return nil, fmt.Errorf("ambiguous sandbox snapshot fields")
	}
	for _, key := range []string{"version", "primary", "generation", "members"} {
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("missing sandbox snapshot field")
		}
	}
	if members := bytes.TrimSpace(fields["members"]); len(members) == 0 || members[0] != '[' {
		return nil, fmt.Errorf("sandbox membership is not an explicit array")
	}
	type plainSnapshot SandboxSnapshot
	var plain plainSnapshot
	if err := json.Unmarshal(raw, &plain); err != nil {
		return nil, fmt.Errorf("decode sandbox snapshot: %w", err)
	}
	snapshot := SandboxSnapshot(plain)
	if err := snapshot.Check(selector, byteBudget); err != nil {
		return nil, err
	}
	return snapshot.Clone(), nil
}

// UnmarshalJSON applies the SAME strict codec at nested server request
// boundaries, before ordinary typed JSON decoding can discard ambiguity.
func (s *SandboxSnapshot) UnmarshalJSON(raw []byte) error {
	decoded, err := DecodeSandboxSnapshot(raw, "", len(raw))
	if err != nil {
		return err
	}
	*s = *decoded
	return nil
}
