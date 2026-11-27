package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// PolicyNextLayersV1 records only pending laptop-source data. A protected
// manifest keeps its raw JSON, including unsupported future capsules. This
// value cannot authorise native boot, secrets, mounts or an SSH key.
type PolicyNextLayersV1 struct {
	Version int     `json:"v"`
	Layers  []Layer `json:"layers"`
}

// EncodePolicyNextLayersV1 owns the complete selected laptop vector.
func EncodePolicyNextLayersV1(layers []Layer) (json.RawMessage, error) {
	selected, err := PolicyLaptopLayersV1(layers)
	if err != nil || len(selected) == 0 || len(selected) != len(layers) {
		return nil, ErrPolicyLiveV1
	}
	raw, err := json.Marshal(PolicyNextLayersV1{Version: PolicyReplaceVersionV1, Layers: selected})
	if err != nil {
		return nil, fmt.Errorf("encode next-start policy sources: %w. Try: check the selected policy layers", err)
	}
	return raw, nil
}

// DecodePolicyNextLayersV1 is a closed read-only projection. An unsupported
// capsule stays opaque in its owner and must never be rewritten from this view.
func DecodePolicyNextLayersV1(raw json.RawMessage) (*PolicyNextLayersV1, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !ExactJSONFields(raw, PolicyNextLayersV1{}) {
		return nil, ErrPolicyLiveV1
	}
	var next PolicyNextLayersV1
	if json.Unmarshal(raw, &next) != nil || next.Version != PolicyReplaceVersionV1 {
		return nil, ErrPolicyLiveV1
	}
	selected, err := PolicyLaptopLayersV1(next.Layers)
	if err != nil || len(selected) == 0 || len(selected) != len(next.Layers) {
		return nil, ErrPolicyLiveV1
	}
	next.Layers = selected
	return &next, nil
}

// DesiredPolicyLayersV1 rebuilds DATA from the current protected excluded
// layers. It cannot resurrect a stale session grant from a pending capsule.
// Native and secret consumers must retain their actual original producers.
func DesiredPolicyLayersV1(applied []Layer, raw json.RawMessage) ([]Layer, error) {
	next, err := DecodePolicyNextLayersV1(raw)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return CloneLayers(applied), nil
	}
	return ReplacePolicyLaptopLayersV1(applied, next.Layers)
}

// PolicyNextMatchesAppliedV1 compares the exact selected projection, never a
// hash or claimed readiness. Only the original native owner, after its real
// readiness and unchanged-operation checks, may clear that captured capsule.
func PolicyNextMatchesAppliedV1(applied []Layer, original json.RawMessage) bool {
	next, err := DecodePolicyNextLayersV1(original)
	if err != nil || next == nil {
		return false
	}
	selected, err := PolicyLaptopLayersV1(applied)
	if err != nil {
		return false
	}
	actual, actualErr := json.Marshal(selected)
	expected, expectedErr := json.Marshal(next.Layers)
	return actualErr == nil && expectedErr == nil && bytes.Equal(actual, expected)
}
