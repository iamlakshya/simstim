package backend

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestNitroCommandRequiresExplicitPlanAndFixedPCIEnclave(t *testing.T) {
	spec, paths := bootFixture()
	spec.Driver = "nitro"
	for i := range spec.VM.Runtime.Items {
		item := &spec.VM.Runtime.Items[i]
		if item.Kind == "kernel" {
			item.Name = "guest-efi"
			item.URL = strings.TrimSuffix(item.URL, "vmlinux-amd64") + "kernel-efi-amd64"
		}
		if item.Kind == "vmm" {
			item.Version = "1.18.0"
		}
	}
	args, data, _, err := NitroCommand(spec, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--enable-pci") {
		t.Fatal("Nitro has no PCI transport")
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	var enclave map[string]bool
	if err := json.Unmarshal(config["enclave"], &enclave); err != nil || len(enclave) != 2 || !enclave["direct_mode"] || !enclave["cpu_overcommit"] {
		t.Fatalf("enclave %s %v", config["enclave"], err)
	}
	if string(config["network-interfaces"]) != "[]" || len(config["balloon"]) != 0 || len(config["entropy"]) != 0 {
		t.Fatalf("unexpected device set %s", data)
	}
	spec.Driver = ""
	if _, _, _, err := NitroCommand(spec, paths); err == nil {
		t.Fatal("Nitro inferred from shared VMM bytes")
	}
}
