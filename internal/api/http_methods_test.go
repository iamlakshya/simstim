package api

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestHTTPMethodsPreserveAbsentNullAndExplicitEmpty(t *testing.T) {
	for _, raw := range []string{`{}`, `{"methods":null}`, `{"methods":[]}`} {
		var rule Rule
		if err := json.Unmarshal([]byte(raw), &rule); err != nil {
			t.Fatal(err)
		}
		present := raw == `{"methods":[]}`
		if (rule.Methods != nil) != present {
			t.Fatalf("method presence lost for %s", raw)
		}
		wire, err := json.Marshal(rule)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wire, &fields); err != nil {
			t.Fatal(err)
		}
		_, exists := fields["methods"]
		if exists != present || present && string(fields["methods"]) != "[]" {
			t.Fatalf("presence broadened on round trip: %s", wire)
		}
	}
	var rule Rule
	if _, err := toml.Decode("methods = []", &rule); err != nil || rule.Methods == nil || len(*rule.Methods) != 0 {
		t.Fatalf("TOML empty method set lost: %+v, %v", rule.Methods, err)
	}
	var underlyingNil []string
	for _, value := range []any{Rule{Methods: &underlyingNil}, PolicyAddArgs{Methods: &underlyingNil}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil || string(fields["methods"]) != "[]" {
			t.Fatalf("in-memory constrained empty set broadened to legacy null: %s, %v", data, err)
		}
	}
}

func TestHTTPMethodCanonicalIdentityKeepsLegacyBytes(t *testing.T) {
	rule := Rule{Layer: LayerSession, Door: DoorNet, Target: "api.example.com", Allow: true}
	if got := RuleID(rule); got != "g6jnnd7" {
		t.Fatalf("legacy identity changed: %q", got)
	}
	raw := []string{"POST", "GET", "POST", "get"}
	methods, err := CanonicalHTTPMethods(&raw)
	if err != nil || !slices.Equal(*methods, []string{"GET", "POST", "get"}) || raw[0] != "POST" {
		t.Fatalf("exact-case canonical copy = %v, input=%v, %v", methods, raw, err)
	}
	rule.Methods = &raw
	constrainedID := RuleID(rule)
	reordered := []string{"get", "GET", "POST"}
	rule.Methods = &reordered
	if RuleID(rule) != constrainedID || constrainedID == "g6jnnd7" {
		t.Fatal("method set order changed identity or collided with legacy identity")
	}
	empty := []string{}
	rule.Methods = &empty
	if RuleID(rule) == constrainedID || RuleID(rule) == "g6jnnd7" {
		t.Fatal("explicit empty method identity was broadened")
	}
	layers := CloneLayers([]Layer{{Kind: LayerSession, Rules: []Rule{rule}}})
	if layers[0].Rules[0].Methods == nil || !reflect.DeepEqual(*layers[0].Rules[0].Methods, empty) {
		t.Fatal("layer clone lost empty method presence")
	}
}

func TestHTTPMethodsRejectInvalidTokensWithoutChangingCase(t *testing.T) {
	for _, method := range []string{"", " GET", "GET ", "GET POST", "G\nET", "G\rET", "G\tET", "G/ET", "méthod", "G:ET"} {
		methods := []string{method}
		if _, err := CanonicalHTTPMethods(&methods); err == nil {
			t.Fatalf("invalid method accepted: %q", method)
		}
	}
	for _, method := range []string{"GET", "get", "CUSTOM", "M-SEARCH", "!#$%&'*+-.^_`|~"} {
		methods := []string{method}
		got, err := CanonicalHTTPMethods(&methods)
		if err != nil || (*got)[0] != method {
			t.Fatalf("valid exact token changed: %q, %v", method, err)
		}
	}
}
