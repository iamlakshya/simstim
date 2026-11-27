package host

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestCapabilityProbeKeepsPreparationAndReadinessSeparate(t *testing.T) {
	source := fakeCapSource(t)
	probes := []wall.Probe{
		{Driver: "chv", Level: wall.MicroVM, CanPrepare: true, OK: false, Reason: "compatibility not yet certified"},
		{Driver: "gvisor", Level: wall.GVisor, CanPrepare: false, OK: true},
		{Driver: "process", Level: wall.Process, CanPrepare: false, OK: false, Reason: "runtime unavailable"},
	}
	source.wallProbes = func(context.Context) []wall.Probe { return probes }
	got, err := capabilityProbe(t.Context(), "", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Walls) != len(probes) {
		t.Fatalf("the collector changed the registered driver set: %+v", got.Walls)
	}
	for index, expected := range probes {
		actual := got.Walls[index]
		if actual.Driver != expected.Driver || actual.CanPrepare != expected.CanPrepare || actual.OK != expected.OK || actual.Reason != expected.Reason {
			t.Fatalf("the collector changed preparation or readiness evidence: %+v, want %+v", actual, expected)
		}
	}
}
