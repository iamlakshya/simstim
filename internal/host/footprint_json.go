package host

import "encoding/json"

func decodeFootprint(data []byte, target any, names []string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, target); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range names {
		delete(fields, name)
	}
	return fields, nil
}

func encodeFootprint(value any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for name, value := range extra {
		if _, ok := fields[name]; !ok {
			fields[name] = value
		}
	}
	return json.Marshal(fields)
}

// UnmarshalJSON retains future footprint entry fields.
func (f *Footprint) UnmarshalJSON(data []byte) error {
	type plain Footprint
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"id", "root", "original", "backup", "kind", "path", "before", "after", "at", "status", "owned", "directory", "prepared", "prepared_ref", "quarantine", "pending", "privileged", "owned_link", "link_target", "previous"})
	*f = Footprint(value)
	f.extra = extra
	return err
}

// MarshalJSON preserves future footprint entry fields.
func (f Footprint) MarshalJSON() ([]byte, error) {
	type plain Footprint
	return encodeFootprint(plain(f), f.extra)
}

func (f *footprintLedger) UnmarshalJSON(data []byte) error {
	type plain footprintLedger
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"version", "host_id", "uid", "entries"})
	*f = footprintLedger(value)
	f.extra = extra
	return err
}

func (f footprintLedger) MarshalJSON() ([]byte, error) {
	type plain footprintLedger
	return encodeFootprint(plain(f), f.extra)
}
