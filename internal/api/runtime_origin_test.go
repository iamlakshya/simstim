package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestRuntimeOriginCarrierRejectsDuplicatesAliasesVersionAndOversize(t *testing.T) {
	for _, raw := range []string{
		`{"v":1,"v":1,"manifest":{}}`,
		`{"V":1,"manifest":{}}`,
		`{"v":2,"manifest":{}}`,
		`{"v":1,"manifest":{"release":"x","release":"y","items":[],"advisories":[]}}`,
		`{"v":1,"manifest":{"Release":"x","items":[],"advisories":[]}}`,
		`{"v":1,"manifest":{"items":[{"kind":"vmm","kind":"kernel"}],"advisories":[]}}`,
		`{"v":1,"manifest":null}`,
		`{"manifest":{}}`,
		`{"v":1,"manifest":{"release":"1.0.0","runtime":"r","items":null,"advisories":null}}`,
		`{"v":1,"manifest":{"release":"1.0.0","runtime":"r","items":[null],"advisories":null}}`,
		`{"v":1,"manifest":{"release":"` + strings.Repeat("x", MaxRuntimeOriginV1) + `"}}`,
	} {
		if _, err := DecodeRuntimeOriginV1([]byte(raw)); err == nil {
			t.Fatal("ambiguous or unbounded carrier accepted")
		}
	}
}
func TestRuntimeOriginClonePreservesOriginalVectorShapeAndOwnership(t *testing.T) {
	original := &RuntimeOriginV1{V: 1, Manifest: supply.Manifest{Release: "1.0.0", Runtime: "fixture", Items: []supply.Item{}, Advisories: []supply.Advisory{}}}
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	copy := CloneRuntimeOriginV1(original)
	after, err := json.Marshal(copy)
	if err != nil || string(before) != string(after) || copy.Manifest.Items == nil || copy.Manifest.Advisories == nil {
		t.Fatal("clone normalised original signed metadata vectors", err)
	}
	copy.Manifest.Items = append(copy.Manifest.Items, supply.Item{Name: "changed"})
	if len(original.Manifest.Items) != 0 {
		t.Fatal("clone aliased original item vector")
	}
}

func TestRuntimeOriginNilAndEmptyAdvisoriesKeepDistinctOriginalShapeV1(t *testing.T) {
	item := supply.NewItem(supply.KindUserns, "namespace-launcher", "1.0.0", "amd64", []byte("data"), supply.ReleaseFolder("1.0.0")+"/namespace-amd64")
	for _, advisories := range [][]supply.Advisory{nil, {}} {
		original := RuntimeOriginV1{V: 1, Manifest: supply.Manifest{Release: "1.0.0", Runtime: "fixture", Items: []supply.Item{item}, Advisories: advisories}}
		raw, err := EncodeRuntimeOriginV1(original)
		if err != nil {
			t.Fatal(err)
		}
		if advisories == nil {
			hostile := strings.Replace(string(raw), `"advisories":null`, `"advisories":[null]`, 1)
			if _, err := DecodeRuntimeOriginV1([]byte(hostile)); err == nil {
				t.Fatal("null advisory element accepted")
			}
			hostile = strings.Replace(string(raw), `"advisories":null`, `"advisories":null,"image_defaults":[]`, 1)
			if _, err := DecodeRuntimeOriginV1([]byte(hostile)); err == nil {
				t.Fatal("manifest origin smuggled image defaults")
			}
		}
		decoded, err := DecodeRuntimeOriginV1(raw)
		if err != nil {
			t.Fatal(err)
		}
		if (decoded.Manifest.Advisories == nil) != (advisories == nil) {
			t.Fatal("origin normalised null and empty vectors")
		}
	}
}
