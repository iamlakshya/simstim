package host

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestOriginalPlacementMetadataIsOwnedBeforeNewEffects(t *testing.T) {
	input := NewArgs{Placement: &api.PlacementV1{Version: api.PlacementVersion, PlacedBy: "auto", Reason: "cached inputs and current room"}}
	copied, err := preparePlacementAuditNewV1(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Placement.Reason = "different caller mutation"
	if copied.Placement.Reason == input.Placement.Reason {
		t.Fatal("outside request metadata retained a caller alias")
	}
	m := &Manifest{}
	if err = applyPlacementAuditNewV1(m, copied.Placement); err != nil {
		t.Fatal(err)
	}
	if m.PlacedBy != "auto" || m.PlaceReason != "cached inputs and current room" || m.Life != nil || m.Run.Generation != "" || len(m.VMNeedV1) != 0 || m.RuntimeManifest != nil {
		t.Fatal("audit metadata altered execution sources", m)
	}
	for _, bad := range []*api.PlacementV1{{Version: 2, PlacedBy: "auto", Reason: "future"}, {Version: 1, PlacedBy: "auto", Reason: "two\nlines"}, {Version: 1, PlacedBy: "callback", Reason: "unknown"}} {
		if _, err := preparePlacementAuditNewV1(NewArgs{Placement: bad}); err == nil {
			t.Fatal("unsupported audit metadata reached New effects", bad)
		}
	}
}

func TestPlacementDisplayIsCleanAndDoesNotLabelMembers(t *testing.T) {
	m := &Manifest{PlacedBy: "auto\n", PlaceReason: "why\x1b" + strings.Repeat("x", 300)}
	by, reason := placementAuditInfoV1(m)
	if by != "auto " || reason != api.CleanText(m.PlaceReason, 256) || len([]rune(reason)) != 256 {
		t.Fatal("unclean legacy audit metadata reached display", by, reason)
	}
	m.SandboxOf = "aaaa1111"
	if by, reason := placementAuditInfoV1(m); by != "" || reason != "" {
		t.Fatal("a member claimed primary host selection")
	}
}
