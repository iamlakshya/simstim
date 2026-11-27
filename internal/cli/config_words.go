package cli

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// wants says what a value of the Go type t is in TOML.
func wants(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice:
		switch t.Elem().Kind() {
		case reflect.Struct, reflect.Pointer:
			return "an array of tables"
		case reflect.String:
			return "an array of strings"
		default:
			return "an array"
		}
	case reflect.Map:
		if t.Elem().Kind() == reflect.String {
			return "a table of strings"
		}
		return "a table"
	default:
		return "a table"
	}
}

// valueWords is a value from a file as a finding names it: a string,
// number or boolean as it is, and others by their kind.
func valueWords(v any) string {
	switch v := v.(type) {
	case string:
		if r := []rune(v); len(r) > 40 {
			v = string(r[:40]) + "…"
		}
		return strconv.Quote(v)
	case bool, int64, float64:
		return fmt.Sprint(v)
	case map[string]any:
		return "a table"
	case []any, []map[string]any:
		return "an array"
	default:
		return "a date or a time"
	}
}

// ref names the key at p for a finding: `"motion" in [ui]`, `"name" in
// [[host]]`, or `"motion"` at the top level.
func ref(p keyPath) string {
	return strconv.Quote(p[len(p)-1].key) + in(p[:len(p)-1])
}

// in names the table at p: " in [ui]", " in [[host]]", or "" for the top
// level.
func in(p keyPath) string {
	switch {
	case len(p) == 0:
		return ""
	case p[len(p)-1].at >= 0:
		return " in [[" + p.shown() + "]]"
	default:
		return " in [" + p.shown() + "]"
	}
}

// quoted is each word in quotes, joined as a list with "or".
func quoted(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = strconv.Quote(w)
	}
	if len(q) < 2 {
		return strings.Join(q, "")
	}
	return strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}
