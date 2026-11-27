package vmneed

import (
	"github.com/iamlakshya/simstim/internal/supply"
	"strings"
	"testing"
)

func TestNitroNeedKeepsItsOwnDriverAndSignedEFIRole(t *testing.T) {
	need := firecrackerNeed("amd64")
	need.Driver = "nitro"
	for i := range need.Runtime.Items {
		item := &need.Runtime.Items[i]
		if item.Kind == supply.KindKernel {
			item.Name = "guest-efi"
			item.URL = supply.ReleaseFolder(need.Runtime.Release) + "/kernel-efi-amd64"
		}
	}
	need.CPUs = 64
	if err := need.Validate("amd64"); err != nil {
		t.Fatal(err)
	}
	data, err := EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeV1(data, "amd64")
	if err != nil || decoded.Driver != "nitro" {
		t.Fatal("explicit Nitro Need was lost", err)
	}
	need.Driver = "firecracker"
	if need.Validate("amd64") == nil {
		t.Fatal("KVM accepted Nitro limits or kernel role")
	}
	need.Driver = "nitro"
	for i := range need.Runtime.Items {
		if need.Runtime.Items[i].Kind == supply.KindVMM {
			need.Runtime.Items[i].Version = "1.17.0"
		}
	}
	if err := need.Validate("amd64"); err == nil || !strings.Contains(err.Error(), "1.18.0") {
		t.Fatalf("old runtime accepted: %v", err)
	}
}

func TestNitroRefusesUnrepresentableGuestMemory(t *testing.T) {
	for _, guest := range []int64{1 << 20, 63 << 20, 65 << 20, 257 << 20} {
		boot := firecrackerNeed("amd64").BootV1
		boot.GuestRAM = guest
		for i := range boot.Runtime.Items {
			if boot.Runtime.Items[i].Kind == supply.KindKernel {
				boot.Runtime.Items[i].Name = "guest-efi"
				boot.Runtime.Items[i].URL = supply.ReleaseFolder(boot.Runtime.Release) + "/kernel-efi-amd64"
			}
		}
		if boot.ValidateForDriver("nitro", "amd64") == nil {
			t.Fatalf("accepted unrepresentable memory %d", guest)
		}
	}
}
