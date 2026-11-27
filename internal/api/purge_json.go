package api

import (
	"context"
	"encoding/json"
)

func decodePurge(data []byte, value any, names []string) (map[string]json.RawMessage, error) {
	fields, err := ProjectedJSONObjectFields(context.Background(), data, names...)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return nil, err
	}
	for _, name := range names {
		delete(fields, name)
	}
	return fields, nil
}

func encodePurge(value any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for name, value := range extra {
		if _, exists := fields[name]; !exists {
			fields[name] = value
		}
	}
	return json.Marshal(fields)
}

// UnmarshalJSON retains future report fields in a local removal receipt.
func (p *PurgeResult) UnmarshalJSON(data []byte) error {
	type plain PurgeResult
	var value plain
	extra, err := decodePurge(data, &value, []string{"host_id", "scope", "user", "uid", "arch", "host_complete", "operation_nonce", "done", "removed", "left", "unsaved", "logins", "helper_gone", "guard_removed", "guard_witness"})
	*p = PurgeResult(value)
	p.extra = extra
	return err
}

// MarshalJSON preserves future report fields in a local removal receipt.
func (p PurgeResult) MarshalJSON() ([]byte, error) {
	type plain PurgeResult
	return encodePurge(plain(p), p.extra)
}

// UnmarshalJSON retains future resource outcomes in a local removal receipt.
func (p *PurgeItem) UnmarshalJSON(data []byte) error {
	type plain PurgeItem
	var value plain
	extra, err := decodePurge(data, &value, []string{"kind", "count", "path", "reason", "hint"})
	*p = PurgeItem(value)
	p.extra = extra
	return err
}

// MarshalJSON preserves future resource outcomes in a local removal receipt.
func (p PurgeItem) MarshalJSON() ([]byte, error) {
	type plain PurgeItem
	return encodePurge(plain(p), p.extra)
}

// UnmarshalJSON retains future unsaved-work observations in a removal receipt.
func (p *PurgeSession) UnmarshalJSON(data []byte) error {
	type plain PurgeSession
	var value plain
	extra, err := decodePurge(data, &value, []string{"id", "name", "reason"})
	*p = PurgeSession(value)
	p.extra = extra
	return err
}

// MarshalJSON preserves future unsaved-work observations in a removal receipt.
func (p PurgeSession) MarshalJSON() ([]byte, error) {
	type plain PurgeSession
	return encodePurge(plain(p), p.extra)
}
