package chv

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

func bootFixture() bootSpec {
	folder := supply.ReleaseFolder("1.0.0")
	vmm := supply.NewItem(supply.KindVMM, "cloud-hypervisor", "54.1", "amd64", []byte("vmm"), folder+"/cloud-hypervisor-static")
	vmm.EOL = "2027-01-06"
	vmm.HostKernel = ">=6.8"
	vmm.Released = "2026-11-11"
	kernel := supply.NewItem(supply.KindKernel, "guest", "6.18.63", "amd64", []byte("kernel"), folder+"/vmlinux-amd64")
	kernel.Licence = "GPL-2.0-only"
	kernel.SourceURL = folder + "/linux-6.18.63.tar.xz"
	kernel.Released = "2026-11-10"
	tools := supply.NewItem(supply.KindTools, "tools", "1.0.0", "amd64", []byte("tools"), folder+"/tools-amd64.erofs")
	tools.Size = 202 * 4096
	tools.DataBlocks = 200
	tools.RootHash = strings.Repeat("d", 64)
	tools.Salt = strings.Repeat("0", 64)
	return bootSpec{Runtime: supply.Manifest{Release: "1.0.0", Runtime: "fixture", Items: []supply.Item{vmm, kernel, tools}}, Image: image.Request{Ref: "registry.example/team/dev:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 300}, Paths: bootPaths{VMM: "/store/vmm", Kernel: "/store/kernel", Tools: "/store/tools", Image: "/store/image", Agents: "/store/agents", Session: "/session/vm/disk.img", API: "/run/private/vmm.sock", Vsock: "/run/private/vsock.sock"}, GuestRAM: 4096 * mebibyte, CPUs: 2, SessionDirect: true}
}

func TestCloudHypervisorBootArgvGolden(t *testing.T) {
	spec := bootFixture()
	args, budget, err := bootCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(struct {
		Args   []string     `json:"argv"`
		Budget MemoryBudget `json:"memory"`
	}{args, budget}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/boot.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(append(data, '\n')) != string(want) {
		t.Fatalf("boot argv or trusted verity tables changed:\n%s", data)
	}
}

func TestEveryDiskForcesRawAndDisablesBackingFiles(t *testing.T) {
	spec := bootFixture()
	for _, direct := range []bool{true, false} {
		spec.SessionDirect = direct
		args, budget, err := bootCommand(spec)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, arg := range args {
			if !strings.HasPrefix(arg, "path=") || !strings.Contains(arg, "serial=") {
				continue
			}
			count++
			if !strings.Contains(arg, "image_type=raw,backing_files=off") {
				t.Fatalf("disk format became guest-controlled: %s", arg)
			}
			if !strings.HasSuffix(arg, "serial=session") && !strings.Contains(arg, "direct=on") {
				t.Fatal("an immutable disk became buffered")
			}
			if strings.HasSuffix(arg, "serial=session") && strings.Contains(arg, "direct=on") != direct {
				t.Fatal("session direct-I/O selection changed")
			}
		}
		if count != 4 || budget.Guest != spec.GuestRAM || budget.Limit != 4240*mebibyte {
			t.Fatalf("disk count%d or memory plan%+v", count, budget)
		}
	}
}

func TestBootRejectsMissingPinsAndOptionInjection(t *testing.T) {
	for name, change := range map[string]func(*bootSpec){"no root": func(s *bootSpec) { s.Image.RootHash = ""; s.Image.DataBlocks = 0 }, "bad root": func(s *bootSpec) { s.Image.RootHash = "bad" }, "old VMM": func(s *bootSpec) { s.Runtime.Items[0].Version = "53.1" }, "unknown runtime role": func(s *bootSpec) { s.Runtime.Items[0].Kind = "unknown" }, "disk options": func(s *bootSpec) { s.Paths.Session = "/session/disk,backing_files=on" }, "relative kernel": func(s *bootSpec) { s.Paths.Kernel = "kernel" }, "socket length": func(s *bootSpec) { s.Paths.Vsock = "/" + strings.Repeat("a", 108) }} {
		t.Run(name, func(t *testing.T) {
			spec := bootFixture()
			change(&spec)
			if _, _, err := bootCommand(spec); err == nil {
				t.Fatal("unsafe boot request accepted")
			}
		})
	}
}
