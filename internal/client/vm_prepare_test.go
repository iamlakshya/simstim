package client

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// Metadata only: these are neither built assets nor provenance evidence.
func vmPrepareFixtureNeed() vmneed.NeedV1 {
	folder := supply.ReleaseFolder("1.0.0") + "/"
	vmm := supply.NewItem(supply.KindVMM, "cloud-hypervisor", "54.1", "amd64", []byte("vmm"), folder+"cloud-hypervisor-static")
	vmm.EOL, vmm.Released, vmm.HostKernel = "2027-01-06", "2026-11-11", ">=6.8"
	kernel := supply.NewItem(supply.KindKernel, "guest", "6.18.63", "amd64", []byte("kernel"), folder+"vmlinux-amd64")
	kernel.Licence, kernel.SourceURL, kernel.Released = "GPL-2.0-only", folder+"linux-6.18.63.tar.xz", "2026-11-10"
	tools := supply.NewItem(supply.KindTools, "tools", "1.0.0", "amd64", []byte("tools"), folder+"tools-amd64.erofs")
	tools.Size, tools.DataBlocks, tools.RootHash, tools.Salt = 202*4096, 200, strings.Repeat("d", 64), strings.Repeat("0", 64)
	return vmneed.NeedV1{BootV1: vmneed.BootV1{Version: 1, GuestRAM: 1 << 30, Runtime: supply.Manifest{Release: "1.0.0", Runtime: "fixture", Items: []supply.Item{vmm, kernel, tools}}, Image: image.Request{Ref: "registry.example/fixture:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 300}, Agents: supply.ContentRef{Name: "agents", SHA256: strings.Repeat("e", 64), Size: 4096}}, Driver: "chv", CPUs: 2, DiskMax: 16 << 20}
}

func TestExplicitVMFirstPrepareDoesNotRequireCompatibilityCacheOrPromoteAuto(t *testing.T) {
	need := vmPrepareFixtureNeed()
	probe := api.CapProbe{OS: "linux", Arch: "amd64", Walls: []api.WallProbe{{Driver: "chv", Level: "microvm", CanPrepare: true}}}
	if err := checkVMPrepareCandidateV1(need, "microvm", probe); err != nil {
		t.Fatal("explicit first construction was circularly gated by old boot cache", err)
	}
	if checkVMPrepareCandidateV1(need, "auto", probe) == nil {
		t.Fatal("construction candidate inflated automatic readiness")
	}
	probe.Walls[0].CanPrepare = false
	if checkVMPrepareCandidateV1(need, "microvm", probe) == nil {
		t.Fatal("missing construction prerequisites were bypassed")
	}
	probe.Walls[0].OK = true
	if err := checkVMPrepareCandidateV1(need, "auto", probe); err != nil {
		t.Fatal("actual compatible runtime refused", err)
	}
}

func TestVMFirstPrepareNeverUsesHostStatusAsPinOrArchAuthority(t *testing.T) {
	need := vmPrepareFixtureNeed()
	probe := api.CapProbe{OS: "linux", Arch: "amd64", Walls: []api.WallProbe{{Driver: "chv", Level: "microvm", CanPrepare: true}}}
	need.Image.RootHash = ""
	if checkVMPrepareCandidateV1(need, "microvm", probe) == nil {
		t.Fatal("host availability replaced missing client root hash")
	}
	need = vmPrepareFixtureNeed()
	probe.Arch = "arm64"
	if checkVMPrepareCandidateV1(need, "microvm", probe) == nil {
		t.Fatal("another architecture's client Need accepted")
	}
	need = vmPrepareFixtureNeed()
	probe.Arch = "amd64"
	probe.Walls[0].Driver = "other"
	if checkVMPrepareCandidateV1(need, "microvm", probe) == nil {
		t.Fatal("other registered driver donated its construction result")
	}
}
