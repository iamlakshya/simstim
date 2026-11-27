package api

import (
	"encoding/json"
	"fmt"
)

// MarshalJSON preserves a constrained empty set even when a caller constructed
// a nonnil Methods pointer whose underlying slice is nil. Null is reserved for
// legacy unrestricted semantics, so this normalisation must precede the wire.
func (r Rule) MarshalJSON() ([]byte, error) {
	methods, err := CanonicalHTTPMethods(r.Methods)
	if err != nil {
		return nil, err
	}
	r.Methods = methods
	type wireRule Rule
	data, err := json.Marshal(wireRule(r))
	if err != nil {
		return nil, fmt.Errorf("marshal policy rule: %w", err)
	}
	return data, nil
}

// MarshalJSON applies the same presence contract to a live policy mutation.
func (a PolicyAddArgs) MarshalJSON() ([]byte, error) {
	methods, err := CanonicalHTTPMethods(a.Methods)
	if err != nil {
		return nil, err
	}
	a.Methods = methods
	type wireArgs PolicyAddArgs
	data, err := json.Marshal(wireArgs(a))
	if err != nil {
		return nil, fmt.Errorf("marshal policy mutation: %w", err)
	}
	return data, nil
}
