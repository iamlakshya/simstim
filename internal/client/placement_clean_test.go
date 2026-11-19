package client

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlacementDisplayCleansOnlyItsDrawnCopy(t *testing.T) {
	row := Session{ID: "place234", PlacedBy: "auto\u202e", PlaceReason: "chosen\x1b]52;c;value\x07\u009b\nfrom\u200b policy"}
	original := row
	drawn := CleanSession(row)
	if drawn.PlacedBy != "auto" || drawn.PlaceReason != "chosen]52;c;value from policy" || strings.ContainsAny(drawn.PlaceReason, "\x1b\x07\u009b\u202e\u200b\n") {
		t.Fatalf("placement metadata reached the terminal unchanged: by=%q reason=%q", drawn.PlacedBy, drawn.PlaceReason)
	}
	if !reflect.DeepEqual(row, original) || drawn.ID != original.ID {
		t.Fatal("drawing placement metadata changed the original session row")
	}
}
