package client

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
)

func TestClientHealthChangesOnlyAdvisoryRows(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	facts := hostFactsFixtureValue()
	facts.At, facts.RuntimeDigestsKnown = now.Add(-6*time.Minute), true
	facts.Health = []api.Health{{ID: "native-block", Severity: "blocks_new", Message: "native", Hint: "inspect"}}
	before := cloneHostFacts(facts)
	host := Host{Prepared: &PreparedHost{Level: "microvm", Arch: "amd64"}}
	release := supply.Manifest{Items: []supply.Item{{Kind: supply.KindVMM, Arch: "amd64", SHA256: strings.Repeat("b", 64)}}}
	appendClientHostHealthV1(&facts, host, &release, now, now.Add(time.Second))
	want := map[string]bool{"native-block": true, "level-drift": true, "runtime-missing": true, "clock": true}
	if len(facts.Health) != len(want) {
		t.Fatal("health rows lost or repeated", facts.Health)
	}
	for _, row := range facts.Health {
		if !want[row.ID] {
			t.Fatal("unexpected row", row)
		}
	}
	facts.Health = before.Health
	if !reflect.DeepEqual(facts, before) {
		t.Fatal("health decoration changed capacity, readiness or source facts")
	}
}

func TestClientClockHealthUsesBothLocalEndpoints(t *testing.T) {
	now := time.Now()
	facts := api.HostFacts{At: now.Add(10 * time.Minute)}
	appendClientHostHealthV1(&facts, Host{}, nil, now, now.Add(11*time.Minute))
	if len(facts.Health) != 0 {
		t.Fatal("slow reply fabricated clock drift", facts.Health)
	}
}
