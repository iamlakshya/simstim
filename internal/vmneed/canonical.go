package vmneed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// encoding/json accepts case aliases and duplicate keys. The security object
// must have one spelling and one value at every known nested struct field.
func canonicalV1(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return canonicalValue(decoder, reflect.TypeFor[NeedV1]())
}

func canonicalValue(decoder *json.Decoder, expected reflect.Type) error {
	for expected.Kind() == reflect.Pointer {
		expected = expected.Elem()
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		if expected.Kind() != reflect.Struct {
			return fmt.Errorf("the VM Need has an unexpected object")
		}
		fields := canonicalFields(expected)
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return fmt.Errorf("the VM Need has an invalid field")
			}
			field, known := fields[key]
			if !known || seen[key] {
				return fmt.Errorf("the VM Need has an unknown, aliased or duplicate field%q", key)
			}
			seen[key] = true
			if err := canonicalValue(decoder, field); err != nil {
				return err
			}
		}
	case '[':
		if expected.Kind() != reflect.Slice && expected.Kind() != reflect.Array {
			return fmt.Errorf("the VM Need has an unexpected array")
		}
		for decoder.More() {
			if err := canonicalValue(decoder, expected.Elem()); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("the VM Need has an unexpected delimiter")
	}
	_, err = decoder.Token()
	return err
}

func canonicalFields(typ reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if field.Anonymous && tag == "" {
			for key, value := range canonicalFields(field.Type) {
				fields[key] = value
			}
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = field.Type
	}
	return fields
}
