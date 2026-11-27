package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"slices"
	"strings"
	"testing"
)

func TestNitroRootPlanIsFixedAndSlotTargetBound(t *testing.T) {
	ids := []RootStepID{RootNitroACL, RootNitroUdev, RootNitroSlots}
	script, err := BuildRootScript("fixture", 1000, ids, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"nitro-acl", "nitro-udev", "nitro-slots", "/dev/nitro_enclaves", "'20'", "slots.before", "donation retained until reboot"} {
		if !strings.Contains(script.Text+script.Undo, part) {
			t.Fatalf("missing %q", part)
		}
	}
	if strings.Contains(script.Text, "@NITRO_SLOTS@") {
		t.Fatal("unexpanded slot target")
	}
	if _, err := BuildRootScript("fixture", 1000, ids, 0); err == nil {
		t.Fatal("missing target accepted")
	}
	if _, err := BuildRootScript("fixture", 1000, []RootStepID{RootNitroACL}, 20); err == nil {
		t.Fatal("unreviewed slot effect accepted")
	}
	p := api.CapProbe{OS: "linux", User: "fixture", UID: 1000, HasSetfacl: true, Linger: true, Subuid: true, IOURingDisabled: 2, VsockBlocked: true, Nitro: &api.Nitro{Present: true, AccessRestricted: true, Pools: map[string]int64{"pcie_slots": 4}}, Walls: []api.WallProbe{{Driver: "nitro", Level: "microvm"}}}
	got := RootStepsFor(p, api.RuntimeStatus{}, false, 20)
	for _, id := range ids {
		if !slices.Contains(got, id) {
			t.Fatalf("missing step %s in %v", id, got)
		}
	}
	p.HasSetfacl = false
	if len(RootStepsFor(p, api.RuntimeStatus{}, false, 20)) != 0 {
		t.Fatal("Nitro offered without ACL tool")
	}
}

func TestNitroPreferencePreservesExplicitDriver(t *testing.T) {
	p := api.CapProbe{OS: "linux", Kernel: "6.18.63", KVM: api.KVM{Usable: true}, Nitro: &api.Nitro{Present: true, Usable: true}, Walls: []api.WallProbe{{Driver: "chv", Level: "microvm", OK: true}, {Driver: "firecracker", Level: "microvm", OK: true}, {Driver: "nitro", Level: "microvm", CanPrepare: true}}}
	if name, err := selectVMDriverV1(p, "", "microvm"); err != nil || name != "nitro" {
		t.Fatalf("selected %s %v", name, err)
	}
	if name, err := selectVMDriverV1(p, "firecracker", "microvm"); err != nil || name != "firecracker" {
		t.Fatalf("explicit selection %s %v", name, err)
	}
	if name, err := selectVMDriverV1(p, "", "auto"); err != nil || name != "chv" {
		t.Fatalf("unproven compatibility used for auto: %s %v", name, err)
	}
}
