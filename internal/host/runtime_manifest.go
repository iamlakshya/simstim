package host

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// runtimeJSONNames projects the ACTUAL DTO. New known runtime fields must not
// become opaque merely because an older copied field list omitted them.
func runtimeJSONNames() []string {
	kind := reflect.TypeFor[Runtime]()
	names := make([]string, 0, kind.NumField())
	for n := 0; n < kind.NumField(); n++ {
		field := kind.Field(n)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		names = append(names, name)
	}
	return names
}

// UnmarshalJSON retains opaque runtime fields through the ONE canonical
// projected object barrier; duplicated/aliased known fields never authorise.
func (r *Runtime) UnmarshalJSON(data []byte) error {
	names := runtimeJSONNames()
	fields, err := api.JSONObjectFields(data, names...)
	if err != nil {
		return fmt.Errorf("decode session runtime fields: %w", err)
	}
	type plain Runtime
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode session runtime: %w", err)
	}
	for _, name := range names {
		delete(fields, name)
	}
	*r = Runtime(decoded)
	r.extra = fields
	return nil
}

// MarshalJSON preserves opaque data without overriding ANY actual typed
// field, including a typed field omitted by omitempty in this particular row.
func (r Runtime) MarshalJSON() ([]byte, error) {
	type plain Runtime
	data, err := json.Marshal(plain(r))
	if err != nil {
		return nil, fmt.Errorf("encode session runtime: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	known := make(map[string]bool)
	for _, name := range runtimeJSONNames() {
		known[name] = true
	}
	for name, raw := range r.extra {
		if !known[name] {
			fields[name] = raw
		}
	}
	return json.Marshal(fields)
}
