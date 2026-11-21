package jail

import (
	"path"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestMappedControllerPublicLayoutHasOnlyClosedGuestRoles(t *testing.T) {
	probe := mappedControllerPublicV1{ID: "gvisorprobe", Role: "probe-true", Sources: 1, CPUs: 1, MemoryMax: 512 << 20, HostMemory: 4 << 30}
	code, extent, err := mappedControllerExtentV1(probe)
	if err != nil || code != 7 || extent != 12 {
		t.Fatal("fixed public smoke layout refused", code, extent, err)
	}
	guest := probe
	guest.ID, guest.Role, guest.HelperGuest = "aaaa2222", "guest-init", path.Join("/opt", brand.Name, "bin", brand.HostBinary)
	code, extent, err = mappedControllerExtentV1(guest)
	if err != nil || code != 8 || extent != 13 {
		t.Fatal("fixed public guest layout refused", code, extent, err)
	}
	for _, change := range []func(*mappedControllerPublicV1){
		func(v *mappedControllerPublicV1) { v.Role = "agent-command" },
		func(v *mappedControllerPublicV1) { v.HelperGuest = "/bin/sh" },
		func(v *mappedControllerPublicV1) { v.CPUs = 0 },
		func(v *mappedControllerPublicV1) { v.CPUs = 257 },
		func(v *mappedControllerPublicV1) { v.MemoryMax = 0 },
		func(v *mappedControllerPublicV1) { v.HostMemory = 0 },
		func(v *mappedControllerPublicV1) { v.HostMemory = 1 << 61 },
		func(v *mappedControllerPublicV1) { v.Sources = -1 },
		func(v *mappedControllerPublicV1) { v.Sources = 1001 },
		func(v *mappedControllerPublicV1) { v.ID = "../foreign" },
	} {
		bad := guest
		change(&bad)
		if _, _, err := mappedControllerExtentV1(bad); err == nil {
			t.Fatal("foreign role, resource or descriptor extent passed", bad)
		}
	}
	if _, err := sealMappedControllerStateV1(probe, nil); err == nil {
		t.Fatal("missing actual public native vector became a sealed controller state")
	}
	if _, err := sealMappedControllerStateV1(mappedControllerPublicV1{}, nil); err == nil {
		t.Fatal("zero data became a mapped controller state")
	}
}
