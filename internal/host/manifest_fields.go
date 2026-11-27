package host

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

func manifestObject(raw []byte) (map[string]json.RawMessage, []string, error) {
	names := []string{vmNeedField}
	kind := reflect.TypeFor[Manifest]()
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
	fields, err := api.JSONObjectFields(raw, names...)
	return fields, names, err
}

func appendStoredVMNeed(m *Manifest, body []byte) ([]byte, error) {
	if len(m.VMNeedV1) == 0 {
		return body, nil
	}
	// This is the same canonical strict codec as incoming and restore paths.
	// Unknown future bytes remain opaque and exact; they are never executable.
	if err := validateStoredVMNeed(m, m.VMNeedV1); err != nil {
		return nil, err
	}
	if len(body) == 0 || body[len(body)-1] != '}' {
		return nil, fmt.Errorf("invalid manifest JSON body")
	}
	out := append([]byte(nil), body[:len(body)-1]...)
	if len(out) > 1 {
		out = append(out, ',')
	}
	out = append(out, `"vm_need_v1":`...)
	out = append(out, m.VMNeedV1...)
	return append(out, '}'), nil
}
