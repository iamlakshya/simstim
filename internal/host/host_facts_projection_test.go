package host

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHostFactsWallProjectionKeepsIdentityAndConstructionSeparate(t *testing.T) {
	digest := strings.Repeat("a", 64)
	original := api.WallProbe{Driver: "gvisor", Level: "gvisor", Version: "same-build\nopaque version", OK: false, CanPrepare: true, Reason: "native wait\nuncertain", Fix: "try\tagain", Facts: map[string]string{"runtime_manifest": digest, "opaque\nkey": "opaque\nvalue"}}
	projected, known := projectHostWallFactsV1([]api.WallProbe{original})
	if !known || len(projected) != 1 || projected[0].OK || !projected[0].CanPrepare || projected[0].Version != original.Version || projected[0].Facts["runtime_manifest"] != digest || projected[0].Facts["opaque\nkey"] != "opaque\nvalue" || projected[0].Reason != "native wait uncertain" || projected[0].Fix != "try again" {
		t.Fatalf("metadata projection changed readiness or identity: %+v", projected)
	}
	projected[0].Facts["runtime_manifest"] = "different"
	if original.Facts["runtime_manifest"] != digest {
		t.Fatal("display projection borrowed the actual cache map")
	}
	for _, bad := range []api.WallProbe{{Driver: "gvisor\n", Level: "gvisor"}, {Driver: "gvisor", Level: "future"}} {
		if _, known := projectHostWallFactsV1([]api.WallProbe{bad}); known {
			t.Fatal("an altered execution identity became known metadata", bad)
		}
	}
}
