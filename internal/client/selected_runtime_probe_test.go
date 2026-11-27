package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSelectedRuntimeConstructionDoesNotBecomePhysicalReadiness(t *testing.T) {
	conn := &Conn{}
	probe := api.CapProbe{Walls: []api.WallProbe{{Driver: "gvisor", Level: "gvisor", CanPrepare: true}}}
	if err := conn.checkNewWallConstruction("gvisor", probe); err != nil {
		t.Fatal("verified construction facts cannot reach actual pre-value preparation", err)
	}
	if err := conn.checkRunnableWall("gvisor", probe); err == nil {
		t.Fatal("construction facts fabricated physical compatibility")
	}
	if BestWallLevel(probe).String() != "none" {
		t.Fatal("prospective preparation changed current best-level display")
	}
	probe.Walls[0].Driver = "unrelated"
	if err := conn.checkNewWallConstruction("gvisor", probe); err == nil {
		t.Fatal("another driver donated construction facts to the selected runtime")
	}
}

func TestSelectedRuntimeProbeRejectsUnissuedSelectionBeforeRPC(t *testing.T) {
	request := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), request)
	defer request.Close()
	conn := &Conn{}
	for _, selection := range []*newRuntimeSelection{nil, {conn: &Conn{}}} {
		if _, err := conn.capProbeForSelectedRuntime(ctx, selection); err == nil {
			t.Fatal("raw/foreign selection reached a selected-runtime probe")
		}
	}
	if conn.nextID.Load() != 0 {
		t.Fatal("a refused selection allocated a protocol request")
	}
}
