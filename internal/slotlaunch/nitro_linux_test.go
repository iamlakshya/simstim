//go:build linux

package slotlaunch

import (
	"encoding/json"
	"errors"
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"slices"
	"testing"
)

func TestNitroNativeTemplateUsesPCIAndSameFixedRoles(t *testing.T) {
	request := validStart()
	request.Driver = "nitro"
	request.CPUs = 64
	args, data, err := fixedCommand(request, "/private/guest")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--enable-pci") {
		t.Fatal("Nitro used MMIO")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var enclave map[string]bool
	if err := json.Unmarshal(fields["enclave"], &enclave); err != nil || len(enclave) != 2 || !enclave["direct_mode"] || !enclave["cpu_overcommit"] {
		t.Fatalf("wrong native enclave %s %v", fields["enclave"], err)
	}
	if string(fields["network-interfaces"]) != "[]" || len(fields["balloon"]) != 0 {
		t.Fatal("native Nitro gained forbidden device")
	}
	request.Driver = "firecracker"
	request.CPUs = 2
	args, data, err = fixedCommand(request, "/private/guest")
	if err != nil || slices.Contains(args, "--enable-pci") {
		t.Fatal("KVM Firecracker transport changed", err)
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	// Decode into a fresh map: json.Unmarshal preserves prior map keys.
	fields = nil
	_ = json.Unmarshal(data, &fields)
	if _, ok := fields["enclave"]; ok {
		t.Fatal("KVM selected a Nitro device")
	}
}

func TestOldParentKernelGetsExactActionableFailure(t *testing.T) {
	failure := nitroStartError(errors.New("Failed to query hypervisor-supported flags: inappropriate ioctl for device"))
	var typed *launchError
	if !errors.As(failure, &typed) || typed.Code != "wall_unavailable" || typed.Hint != nitrodev.ParentKernelHint {
		t.Fatalf("wrong failure %#v", failure)
	}
}
