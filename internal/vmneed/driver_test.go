package vmneed

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func firecrackerNeed(arch string) NeedV1 {
	need := fixtureNeed()
	need.Driver = "firecracker"
	need.Image.Arch = arch
	for i := range need.Runtime.Items {
		need.Runtime.Items[i].Arch = arch
	}
	folder := supply.ReleaseFolder(need.Runtime.Release) + "/"
	need.Runtime.Items[0].URL = folder + "firecracker-" + arch
	need.Runtime.Items[1].URL = folder + "vmlinux-amd64"
	if arch == "arm64" {
		need.Runtime.Items[1].URL = folder + "Image-arm64"
	}
	need.Runtime.Items[2].URL = folder + "tools-" + arch + ".erofs"
	vmm := &need.Runtime.Items[0]
	vmm.Name, vmm.Version = "firecracker", "1.18.0"
	vmm.HostKernel = "5.10,6.1,6.18"
	return need
}

func TestFirecrackerNeedUsesSameCanonicalWire(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			need := firecrackerNeed(arch)
			for _, cpus := range []int{1, 3, 32} {
				need.CPUs = cpus
				data, err := EncodeV1(need, arch)
				if err != nil {
					t.Fatalf("valid count %d: %v", cpus, err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 8 {
					t.Fatalf("changed Need format: %s, %v", data, err)
				}
				got, err := DecodeV1(data, arch)
				if err != nil || got.Driver != "firecracker" || got.CPUs != cpus {
					t.Fatalf("roundtrip: %+v, %v", got, err)
				}
				budget, err := BudgetFor(need.GuestRAM)
				if err != nil || got.ValidateWall(arch, "microvm", "firecracker", budget.Limit) != nil {
					t.Fatal("the original memory/wall binding changed", err)
				}
			}
		})
	}
}

func TestSelectedDriverDoesNotBorrowAnotherRuntime(t *testing.T) {
	for name, change := range map[string]func(*NeedV1){
		"unknown driver":      func(n *NeedV1) { n.Driver = "unknown" },
		"different VMM":       func(n *NeedV1) { n.Runtime.Items[0].Name = "cloud-hypervisor" },
		"old VMM":             func(n *NeedV1) { n.Runtime.Items[0].Version = "1.16.1" },
		"CPU zero":            func(n *NeedV1) { n.CPUs = 0 },
		"CPU above KVM bound": func(n *NeedV1) { n.CPUs = 33 },
		"extra alternative VMM": func(n *NeedV1) {
			other := n.Runtime.Items[0]
			other.Name = "cloud-hypervisor"
			n.Runtime.Items = append(n.Runtime.Items, other)
		},
	} {
		t.Run(name, func(t *testing.T) {
			need := firecrackerNeed("amd64")
			change(&need)
			if _, err := EncodeV1(need, "amd64"); err == nil {
				t.Fatal("selected driver admitted another or invalid runtime")
			}
		})
	}
	need := firecrackerNeed("amd64")
	if need.BootV1.Validate("amd64") == nil {
		t.Fatal("the CHV compatibility wrapper admitted a different VMM")
	}
	need.Runtime.Items[0].Version = "1.17.0"
	if need.Validate("amd64") != nil {
		t.Fatal("the specified Firecracker runtime floor was raised")
	}
}

func TestDriverRoleCopiesAndExistingLimits(t *testing.T) {
	roles, err := RolesForDriver("firecracker")
	if err != nil || len(roles) != 3 || roles[0].Name != "firecracker" {
		t.Fatal("missing selected role", roles, err)
	}
	roles[0].Name = "changed"
	next, err := RolesForDriver("firecracker")
	if err != nil || next[0].Name != "firecracker" || Roles()[0].Name != "cloud-hypervisor" {
		t.Fatal("role vectors alias or change the CHV wrapper", err)
	}
	if _, err := RolesForDriver("process"); err == nil {
		t.Fatal("a non-VM driver acquired VM roles")
	}
	for _, tc := range []struct {
		arch string
		max  int
	}{{"amd64", 8192}, {"arm64", 255}} {
		if ValidateLimits(tc.max, 1, tc.arch) != nil || ValidateLimits(tc.max+1, 1, tc.arch) == nil {
			t.Fatal("CHV CPU limit changed", tc)
		}
		if ValidateLimitsForDriver("firecracker", 32, 1, tc.arch) != nil || ValidateLimitsForDriver("firecracker", 33, 1, tc.arch) == nil {
			t.Fatal("Firecracker CPU limit changed", tc)
		}
	}
}
