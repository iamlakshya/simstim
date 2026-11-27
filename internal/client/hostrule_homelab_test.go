package client

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHostRuleHomelabAdviceUsesFreshProbeWithoutGrantingCapability(t *testing.T) {
	probe := api.CapProbe{OS: "linux", Arch: "amd64", Virt: "kvm", Walls: []api.WallProbe{{Level: "process", OK: true}}}
	before := probe
	before.Walls = slices.Clone(probe.Walls)
	checks := hostRuleVirtualisationChecks(probe)
	if len(checks) != 1 || checks[0].OK || !strings.Contains(checks[0].Fix, "qm set <vmid> --cpu host") {
		t.Fatal("a VM without KVM or processor flags has no actionable CPU hint", checks)
	}
	if !reflect.DeepEqual(probe, before) || len(probe.Walls) != 1 || probe.Walls[0].Level != "process" {
		t.Fatal("display advice changed capability or wall metadata")
	}
}

func TestHostRuleHomelabAdviceDoesNotConfusePermissionsOrOtherArchitectures(t *testing.T) {
	for _, probe := range []api.CapProbe{
		{OS: "darwin", Arch: "amd64"},
		{OS: "linux", Arch: "arm64"},
		{OS: "linux", Arch: "amd64", KVM: api.KVM{Present: true, Usable: false}},
		{OS: "linux", Arch: "amd64", CPUVirt: "Intel VT-x"},
		{OS: "linux", Arch: "amd64", CPUVirt: "AMD-V"},
	} {
		if got := hostRuleVirtualisationChecks(probe); len(got) != 0 {
			t.Fatal("a permission problem or non-x86 platform received misleading CPU advice", probe, got)
		}
	}
}

func TestHostRuleContainerAdviceNeverSuggestsPrivilegedDevices(t *testing.T) {
	for _, kind := range []string{"lxc", "docker", "container"} {
		checks := hostRuleVirtualisationChecks(api.CapProbe{OS: "linux", Arch: "amd64", Virt: kind})
		if len(checks) != 1 || !strings.Contains(checks[0].Fix, "Do not make the container privileged") || !strings.Contains(checks[0].Fix, "Run the helper in a VM") {
			t.Fatal("container advice did not preserve the host boundary", checks)
		}
	}
}
