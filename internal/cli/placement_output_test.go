package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestSessionJSONKeepsActualPlacementMetadataWithoutMakingADrawnCopy(t *testing.T) {
	row := client.Session{ID: "place234", PlacedBy: "auto\u202e", PlaceReason: "chosen\u009b\u200b by the saved policy"}
	original := row
	data, err := versioned(sessionObject("box", row))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		PlacedBy    string `json:"placed_by"`
		PlaceReason string `json:"place_reason"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.PlacedBy != original.PlacedBy || result.PlaceReason != original.PlaceReason || !reflect.DeepEqual(row, original) {
		t.Fatalf("machine output changed advisory placement metadata: %+v %v", result, err)
	}
	for _, r := range string(data) {
		if r != '\n' && r != '\t' && api.Unclean(r) {
			t.Fatalf("machine JSON wrote raw terminal control %U", r)
		}
	}
	empty, err := versioned(sessionObject("box", client.Session{ID: "place234"}))
	if err != nil || strings.Contains(string(empty), "placed_by") || strings.Contains(string(empty), "place_reason") {
		t.Fatalf("an absent producer gained placement history: %s %v", empty, err)
	}
}
