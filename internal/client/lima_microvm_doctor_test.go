package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"strings"
	"testing"
)

func TestLimaMicroVMDoctorUsesActualGuestKVMAndNeverChangesFloor(t *testing.T) {
	host := Host{Name: "box", WallFloor: "microvm", Machine: &Machine{Provider: "lima"}}
	probe := api.CapProbe{OS: "linux", Arch: "arm64", Walls: []api.WallProbe{{Driver: "chv", Level: "microvm", OK: true}}}
	checks := limaMicroVMGuestChecksV1(host, probe, "darwin", "arm64")
	if len(checks) != 1 || checks[0].OK || !strings.Contains(checks[0].Detail, "M3") || !strings.Contains(checks[0].Detail, "macOS 15") {
		t.Fatal(checks)
	}
	if host.WallFloor != "microvm" || !probe.Walls[0].OK {
		t.Fatal("diagnosis changed the floor or fabricated a probe")
	}
	probe.KVM.Present = true
	if len(limaMicroVMGuestChecksV1(host, probe, "darwin", "arm64")) != 0 {
		t.Fatal("access restriction became a hardware claim")
	}
	probe.KVM.Usable = true
	if len(limaMicroVMGuestChecksV1(host, probe, "darwin", "arm64")) != 0 {
		t.Fatal("working KVM got an unsupported-hardware warning")
	}
	probe.KVM = api.KVM{}
	if len(limaMicroVMGuestChecksV1(host, probe, "linux", "arm64")) != 0 {
		t.Fatal("Linux Lima got a Mac-only restriction")
	}
}
