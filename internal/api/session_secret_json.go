package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

// ExactJSONFields checks exact JSON tags and duplicate fields against the
// supplied sole DTO type. It confers no value or name authority. Callers still
// perform their ordinary typed decode and semantic validation. Unlike the
// standard decoder it refuses case aliases, including nested struct fields.
func ExactJSONFields(raw []byte, shape any) bool {
	if shape == nil || !json.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	return exactSecretStateValue(decoder, reflect.TypeOf(shape))
}

// ExactJSONFieldNames returns owned, sorted field names from the same DTO
// descriptor used by ExactJSONFields. Streaming projections can build their
// finite schema once per inspection without copying a second field grammar.
// It validates no source, value, grant or provenance.
func ExactJSONFieldNames(shape any) []string {
	if shape == nil {
		return nil
	}
	kind := reflect.TypeOf(shape)
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	fields := secretStateFields(kind)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func exactSecretStateValue(decoder *json.Decoder, shape reflect.Type) bool {
	for shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	// RawMessage carries a caller-owned schema. Keep every nested duplicate
	// check here; its actual consumer still checks its closed payload type.
	opaque := shape == reflect.TypeFor[json.RawMessage]()
	delim, nested := token.(json.Delim)
	if !nested {
		return true
	}
	switch delim {
	case '{':
		if opaque {
			shape = reflect.TypeFor[map[string]json.RawMessage]()
		}
		if shape.Kind() != reflect.Struct && shape.Kind() != reflect.Map {
			return false
		}
		fields := secretStateFields(shape)
		keys := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return false
			}
			if _, exists := keys[name]; exists {
				return false
			}
			keys[name] = struct{}{}
			var child reflect.Type
			if shape.Kind() == reflect.Map {
				child = shape.Elem()
			} else {
				child = fields[name]
			}
			if child == nil || !exactSecretStateValue(decoder, child) {
				return false
			}
		}
	case '[':
		if shape == reflect.TypeFor[HostSelectorV1]() {
			shape = reflect.TypeFor[[]string]()
		}
		if opaque {
			shape = reflect.TypeFor[[]json.RawMessage]()
		}
		if shape.Kind() != reflect.Slice && shape.Kind() != reflect.Array {
			return false
		}
		for decoder.More() {
			if !exactSecretStateValue(decoder, shape.Elem()) {
				return false
			}
		}
	default:
		return false
	}
	_, err = decoder.Token()
	return err == nil
}

func secretStateFields(shape reflect.Type) map[string]reflect.Type {
	if shape.Kind() != reflect.Struct {
		return nil
	}
	fields := make(map[string]reflect.Type, shape.NumField())
	// encoding/json promotes an anonymous struct with no explicit tag. Direct
	// named fields shadow those promoted fields and are applied afterwards.
	for i := range shape.NumField() {
		field := shape.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.Anonymous || name != "" {
			continue
		}
		nested := field.Type
		for nested.Kind() == reflect.Pointer {
			nested = nested.Elem()
		}
		if nested.Kind() != reflect.Struct {
			continue
		}
		for name, child := range secretStateFields(nested) {
			fields[name] = child
		}
	}
	for i := range shape.NumField() {
		field := shape.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		nested := field.Type
		for nested.Kind() == reflect.Pointer {
			nested = nested.Elem()
		}
		if field.Anonymous && name == "" && nested.Kind() == reflect.Struct {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}
