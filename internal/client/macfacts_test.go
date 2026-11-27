package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/macfacts"
	"testing"
)

func TestMacAdvisoryBatteryMovesExistingPlacementBand(t *testing.T) {
	facts := api.HostFacts{OnBatteryKnown: true, OnBattery: true}
	if placementPressurePenalty(facts) != 1 {
		t.Fatal("Mac battery did not lower the existing rank band")
	}
	facts.OnBatteryKnown = false
	if placementPressurePenalty(facts) != 0 {
		t.Fatal("unknown battery state became an observed battery claim")
	}
}

func TestMacDoctorKeepsCustodyAndPowerWarningsExplicit(t *testing.T) {
	mac := macfacts.Facts{Version: 1, Kind: "mac", MacOS: "27.0", Chip: "Apple M3", OnBatteryKnown: true, OnBattery: true, ACSleepKnown: true, ACSleepMinutes: 5, StartMode: "user-agent", LaunchKnown: true, LaunchEnabled: false}
	rows := macHealthV1(mac)
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.ID] = true
		if row.Severity != "warn" || row.Hint == "" {
			t.Fatal("advisory facts gained a stronger decision or lost its hint")
		}
	}
	for _, id := range []string{"mac-on-battery", "mac-sleep-on", "mac-user-agent", "mac-startup-off"} {
		if !ids[id] {
			t.Fatalf("missing %s", id)
		}
	}
	copy := cloneHostFacts(api.HostFacts{Mac: &mac})
	copy.Mac.Chip = "changed"
	if mac.Chip != "Apple M3" {
		t.Fatal("a cloned advisory row shared mutable source state")
	}
}
