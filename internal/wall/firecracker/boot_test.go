package firecracker

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

func bootFixture() (wall.Spec, bootPaths) {
	folder := supply.ReleaseFolder("1.0.0")
	vmm := supply.NewItem(supply.KindVMM, "firecracker", "1.18.0", "amd64", []byte("vmm"), folder+"/firecracker-amd64")
	vmm.EOL = "2027-11-01"
	vmm.HostKernel = "5.10,6.1,6.18"
	vmm.Released = "2026-11-01"
	kernel := supply.NewItem(supply.KindKernel, "guest", "6.18.63", "amd64", []byte("kernel"), folder+"/vmlinux-amd64")
	kernel.Licence = "GPL-2.0-only"
	kernel.SourceURL = folder + "/linux-6.18.63.tar.xz"
	kernel.Released = "2026-11-01"
	tools := supply.NewItem(supply.KindTools, "tools", "1.0.0", "amd64", []byte("tools"), folder+"/tools-amd64.erofs")
	tools.Size, tools.DataBlocks = 202*4096, 200
	tools.RootHash, tools.Salt = strings.Repeat("d", 64), strings.Repeat("0", 64)
	boot := vmneed.BootV1{
		Version: 1, GuestRAM: 4096 * vmneed.Mebibyte,
		Runtime: supply.Manifest{Release: "1.0.0", Runtime: "fixture", Items: []supply.Item{vmm, kernel, tools}},
		Image:   image.Request{Ref: "registry.example/team/dev:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 300},
		Agents:  supply.ContentRef{Name: "agents", SHA256: strings.Repeat("e", 64), Size: 4096},
	}
	spec := wall.Spec{ID: "abcdefghijklmnopqrstuvwx234567ab", SessionDir: "/session", VM: &boot, CPUs: 2, DiskMax: 16 << 20, MemoryMax: 4240 * vmneed.Mebibyte}
	paths := bootPaths{VMM: "/store/vmm", Kernel: "/store/kernel", Tools: "/store/tools", Image: "/store/image", Agents: "/store/agents", Session: "/session/vm/disk.img", Config: "/session/fc.json", API: "/run/private/vmm.sock", Vsock: "/run/private/vsock.sock"}
	return spec, paths
}

func TestFirecrackerBootKeepsMMIOAndClosedDevices(t *testing.T) {
	spec, paths := bootFixture()
	args, data, budget, err := bootCommand(spec, paths)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{paths.VMM, "--config-file", paths.Config, "--api-sock", paths.API, "--id", spec.ID}
	if !slices.Equal(args, wantArgs) || slices.Contains(args, "--enable-pci") || slices.Contains(args, "--no-seccomp") {
		t.Fatalf("unexpected VMM arguments: %q", args)
	}
	if budget.Guest != 4096*vmneed.Mebibyte || budget.Reserve != 144*vmneed.Mebibyte || budget.Limit != spec.MemoryMax {
		t.Fatalf("changed memory contract: %+v", budget)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 || string(fields["network-interfaces"]) != "[]" {
		t.Fatalf("unexpected device configuration: %s", data)
	}
	var config bootConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Machine.CPUs != 2 || config.Machine.MemoryMiB != 4096 || config.Machine.SMT || config.Machine.DirtyPages {
		t.Fatalf("unexpected machine configuration: %+v", config.Machine)
	}
	if config.Vsock.CID != 3 || config.Vsock.Path != paths.Vsock || config.BootSource.Kernel != paths.Kernel {
		t.Fatalf("changed boot or link devices: %+v", config)
	}
	for i, want := range []struct{ id, path string }{{"tools", paths.Tools}, {"image", paths.Image}, {"agents", paths.Agents}, {"session", paths.Session}} {
		if len(config.Drives) != 4 {
			t.Fatalf("drive count = %d", len(config.Drives))
		}
		got := config.Drives[i]
		if got.ID != want.id || got.Path != want.path || got.Root || got.ReadOnly != (i != 3) || got.Cache != "Writeback" || got.IO != "Sync" {
			t.Fatalf("drive %d = %+v", i, got)
		}
	}
	for _, fragment := range []string{"root=/dev/dm-0", "dm-mod.waitfor=/dev/vda,/dev/vdb", strings.Repeat("d", 64), strings.Repeat("c", 64), "panic_on_corruption", "wall-init --microvm-v1"} {
		if !strings.Contains(config.BootSource.Args, fragment) {
			t.Fatalf("missing boot check %q in %q", fragment, config.BootSource.Args)
		}
	}
}

func TestFirecrackerBootCPUAndResourceRefusals(t *testing.T) {
	for _, cpus := range []int{1, 3, 32} {
		spec, paths := bootFixture()
		spec.CPUs = cpus
		if _, _, _, err := bootCommand(spec, paths); err != nil {
			t.Fatalf("valid count %d with SMT off: %v", cpus, err)
		}
	}
	for name, change := range map[string]func(*wall.Spec, *bootPaths){
		"no original plan":    func(s *wall.Spec, _ *bootPaths) { s.VM = nil },
		"CPU zero":            func(s *wall.Spec, _ *bootPaths) { s.CPUs = 0 },
		"CPU above KVM limit": func(s *wall.Spec, _ *bootPaths) { s.CPUs = 33 },
		"reduced unit cap":    func(s *wall.Spec, _ *bootPaths) { s.MemoryMax-- },
		"old runtime":         func(s *wall.Spec, _ *bootPaths) { s.VM.Runtime.Items[0].Version = "1.16.1" },
		"wrong runtime role":  func(s *wall.Spec, _ *bootPaths) { s.VM.Runtime.Items[0].Name = "cloud-hypervisor" },
		"no root check":       func(s *wall.Spec, _ *bootPaths) { s.VM.Image.RootHash = "" },
		"no agents content":   func(s *wall.Spec, _ *bootPaths) { s.VM.Agents.SHA256 = "" },
		"host share":          func(s *wall.Spec, _ *bootPaths) { s.Mounts = []wall.Mount{{}} },
		"invalid ID":          func(s *wall.Spec, _ *bootPaths) { s.ID = "--enable-pci" },
		"relative kernel":     func(_ *wall.Spec, p *bootPaths) { p.Kernel = "kernel" },
		"socket alias":        func(_ *wall.Spec, p *bootPaths) { p.API = p.Vsock },
		"socket size":         func(_ *wall.Spec, p *bootPaths) { p.API = "/" + strings.Repeat("a", 108) },
		"non-clean path":      func(_ *wall.Spec, p *bootPaths) { p.Config = "/session/../fc.json" },
		"NUL path":            func(_ *wall.Spec, p *bootPaths) { p.Session += "\x00" },
	} {
		t.Run(name, func(t *testing.T) {
			spec, paths := bootFixture()
			change(&spec, &paths)
			if _, _, _, err := bootCommand(spec, paths); err == nil {
				t.Fatal("accepted an invalid boot configuration")
			}
		})
	}
}

func TestFirecrackerBootKeepsPathsAsData(t *testing.T) {
	spec, paths := bootFixture()
	paths.Session = "/session/vm/name,readonly=off and spaces.img"
	_, data, _, err := bootCommand(spec, paths)
	if err != nil {
		t.Fatal(err)
	}
	var got bootConfiguration
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Drives[3].Path != paths.Session {
		t.Fatalf("the data path changed: %s", data)
	}
	if !reflect.DeepEqual(got.NetworkInterfaces, []struct{}{}) {
		t.Fatal("the configuration gained a network device")
	}
}
