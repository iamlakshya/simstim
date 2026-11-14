package runtimecache

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UnmarshalJSON preserves unknown record fields while keeping known supply
// metadata strict. A later metadata refresh cannot erase a parent's fields.
func (r *Record) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode runtime record fields: %w", err)
	}
	known := map[string]json.RawMessage{}
	for _, name := range []string{"manifest", "installed_at", "image_defaults"} {
		if value, ok := fields[name]; ok {
			known[name] = value
			delete(fields, name)
		}
	}
	encoded, err := json.Marshal(known)
	if err != nil {
		return fmt.Errorf("decode known runtime record fields: %w", err)
	}
	type plain Record
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	var parsed plain
	if err := dec.Decode(&parsed); err != nil {
		return fmt.Errorf("decode known runtime record: %w", err)
	}
	*r = Record(parsed)
	r.extra = fields
	return nil
}

// MarshalJSON retains unknown parent record fields without replacing typed
// metadata. Nil image defaults stay absent, while known-empty stays an array.
func (r Record) MarshalJSON() ([]byte, error) {
	type plain Record
	encoded, err := json.Marshal(plain(r))
	if err != nil {
		return nil, fmt.Errorf("encode runtime record: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, fmt.Errorf("encode runtime record fields: %w", err)
	}
	for name, value := range r.extra {
		if _, known := fields[name]; !known {
			fields[name] = value
		}
	}
	encoded, err = json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode complete runtime record: %w", err)
	}
	return encoded, nil
}
