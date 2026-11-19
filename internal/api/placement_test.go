package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlacementMetadataClosesVersionAliasesAndCloneOwnership(t *testing.T) {
	for _, by := range []string{"auto", "explicit"} {
		original := &PlacementV1{Version: PlacementVersion, PlacedBy: by, Reason: "awake, image cached, most room"}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var parsed PlacementV1
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed != *original {
			t.Fatal("valid advisory metadata did not round trip", err)
		}
		copy := ClonePlacementV1(original)
		copy.Reason = "changed"
		if original.Reason == copy.Reason {
			t.Fatal("a request borrowed mutable placement metadata")
		}
	}
	for _, raw := range []string{
		`{"v":2,"placed_by":"auto","reason":"original"}`,
		`{"v":1,"placed_by":"host-grant","reason":"original"}`,
		`{"v":1,"placed_by":"auto","Reason":"alias"}`,
		`{"v":1,"placed_by":"auto","reason":"one","reason":"two"}`,
		`{"v":1,"placed_by":"auto","reason":"first\nsecond"}`,
		`{"v":1,"placed_by":"auto","reason":"original","capacity_grant":true}`,
	} {
		var parsed PlacementV1
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			t.Fatal("invalid advisory metadata became a known original record")
		}
	}
	if err := (PlacementV1{Version: 1, PlacedBy: "auto", Reason: strings.Repeat("x", 257)}).Validate(); err == nil {
		t.Fatal("an unbounded description entered the request record")
	}
}
