package api

import (
	"encoding/json"
	"fmt"
	"slices"
)

// NetworkRule is the structured file spelling of a net rule. Remove deletes
// the exact inherited tuple, preserving Methods nil versus present-empty.
type NetworkRule struct {
	Target  string    `json:"target" toml:"target"`
	Allow   bool      `json:"allow" toml:"allow"`
	Methods *[]string `json:"methods,omitempty" toml:"methods,omitempty"`
	Remove  bool      `json:"remove,omitempty" toml:"remove,omitempty"`
}

// SameHTTPMethods compares canonical presence and exact-case method sets.
func SameHTTPMethods(a, b *[]string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return slices.Equal(*a, *b)
}

// HTTPMethodSetsIntersect reports whether two valid sets share any method.
// Nil is unrestricted. A nonnil empty set shares no method with anything.
func HTTPMethodSetsIntersect(a, b *[]string) bool {
	if a != nil && len(*a) == 0 || b != nil && len(*b) == 0 {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	for _, left := range *a {
		if slices.Contains(*b, left) {
			return true
		}
	}
	return false
}

// MarshalJSON preserves constrained-empty raw/removal tuples before any Layer
// serialisation, including a nonnil pointer to a nil underlying slice.
func (r NetworkRule) MarshalJSON() ([]byte, error) {
	methods, err := CanonicalHTTPMethods(r.Methods)
	if err != nil {
		return nil, err
	}
	r.Methods = methods
	type wireNetworkRule NetworkRule
	data, err := json.Marshal(wireNetworkRule(r))
	if err != nil {
		return nil, fmt.Errorf("marshal structured network rule: %w", err)
	}
	return data, nil
}
