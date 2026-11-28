package client

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// matchStructField matches key against exported struct fields with a toml tag,
// exact match first then case-insensitive, matching go-toml/v2's structPlan.
func matchStructField(t reflect.Type, key string) (tag string, ft reflect.Type, ok bool) {
	var foldTag string
	var foldType reflect.Type
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if name == key {
			return name, f.Type, true
		}
		if foldTag == "" && strings.EqualFold(name, key) {
			foldTag = name
			foldType = f.Type
		}
	}
	if foldTag != "" {
		return foldTag, foldType, true
	}
	return "", nil, false
}

// KnownStructKeys returns the valid TOML key names for the table at path
// within rootType (using exact or case-folded matching along path). Returns
// nil if path enters a free map or an unknown table.
func KnownStructKeys(rootType reflect.Type, tablePath []string) []string {
	t := derefType(rootType)
	for _, seg := range tablePath {
		if t == nil {
			return nil
		}
		if t.Kind() == reflect.Slice {
			t = derefType(t.Elem())
		}
		if t.Kind() != reflect.Struct {
			return nil
		}
		_, ft, ok := matchStructField(t, seg)
		if !ok {
			return nil
		}
		t = derefType(ft)
	}
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Slice {
		t = derefType(t.Elem())
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

// CheckIdleStop validates a machine's idle_stop setting: empty, "never", or a
// duration of at least one minute (shared by host create, host idle and config
// check).
func CheckIdleStop(v string) error {
	if v == "" || v == "never" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute {
		return &UserError{
			Msg:  fmt.Sprintf("%q is not an idle time", v),
			Hint: "Use a duration of a minute or more, such as 30m or 2h, or never.",
			Code: "bad_value",
			Kind: KindUsage,
		}
	}
	return nil
}
