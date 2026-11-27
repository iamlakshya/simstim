package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"testing"
)

func TestPreparedPlacementCapUsesSameCanonicalGvisorSize(t *testing.T) {
	memory := "7G"
	layers := []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Memory: &memory}}}}
	cap, err := preparedPlacementCapV1("gvisor", MemoryAuto, layers, "shell")
	if err != nil || cap != 7<<30 {
		t.Fatal("gvisor default was rejected or invented", cap, err)
	}
	cap, err = preparedPlacementCapV1("gvisor", 2<<30, layers, "shell")
	if err != nil || cap != 2<<30 {
		t.Fatal("explicit native cap lost priority", cap, err)
	}
	if _, err := preparedPlacementCapV1("microvm", MemoryAuto, layers, "shell"); err == nil {
		t.Fatal("VM cap was invented without original Need")
	}
}
