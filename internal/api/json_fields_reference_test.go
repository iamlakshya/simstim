package api

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// referenceJSONObjectFields retains the previous standard decoder as an
// independent compatibility oracle. Production must not call it.
func referenceJSONObjectFields(raw []byte, known ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrJSONObject
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrJSONObject
		}
		name, ok := token.(string)
		if !ok {
			return nil, ErrJSONObject
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, ErrJSONObject
		}
		for _, exact := range known {
			if name != exact && strings.EqualFold(name, exact) {
				return nil, ErrJSONObject
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrJSONObject
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrJSONObject
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrJSONObject
	}
	return fields, nil
}
