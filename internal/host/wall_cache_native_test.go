package host

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestExistingWallCacheBindsBootAndMappedHelperBytes(t *testing.T) {
	data, err := json.Marshal(wallCache{BootID: "boot", Version: "mapped-helper", Walls: []api.WallProbe{{Driver: "process", Level: "process", OK: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if probes := wallProbesFor(data, "boot", "mapped-helper"); len(probes) != 1 || !probes[0].OK {
		t.Fatal("the exact native cache projection was lost", probes)
	}
	for _, identity := range [][2]string{{"old-boot", "mapped-helper"}, {"boot", "other-helper"}, {"", "mapped-helper"}} {
		if probes := wallProbesFor(data, identity[0], identity[1]); len(probes) != 0 {
			t.Fatal("a cache crossed its actual boot/helper generation", identity)
		}
	}
	for _, invalid := range [][]byte{[]byte(`{"boot_id":"boot","version":"mapped-helper","walls":[],"grant":true}`), []byte(`{"boot_id":"boot","version":"mapped-helper"}`), append(data, []byte(`{}`)...)} {
		if probes := wallProbesFor(invalid, "boot", "mapped-helper"); len(probes) != 0 {
			t.Fatal("unknown/trailing cache data became readiness", string(invalid))
		}
	}
}
