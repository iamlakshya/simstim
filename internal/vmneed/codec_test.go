package vmneed

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

// This is metadata only. It is never a binary, image or provenance proof.
func fixtureNeed() NeedV1 {
	folder := supply.ReleaseFolder("1.0.0") + "/"
	vmm := supply.NewItem(supply.KindVMM, "cloud-hypervisor", "54.1", "amd64", []byte("vmm"), folder+"cloud-hypervisor-static")
	vmm.EOL, vmm.Released, vmm.HostKernel = "2027-01-06", "2026-11-11", ">=6.8"
	kernel := supply.NewItem(supply.KindKernel, "guest", "6.18.63", "amd64", []byte("kernel"), folder+"vmlinux-amd64")
	kernel.Licence, kernel.SourceURL, kernel.Released = "GPL-2.0-only", folder+"linux-6.18.63.tar.xz", "2026-11-10"
	tools := supply.NewItem(supply.KindTools, "tools", "1.0.0", "amd64", []byte("tools"), folder+"tools-amd64.erofs")
	tools.Size, tools.DataBlocks, tools.RootHash, tools.Salt = 202*4096, 200, strings.Repeat("d", 64), strings.Repeat("0", 64)
	return NeedV1{BootV1: BootV1{Version: 1, GuestRAM: 1024 * Mebibyte, Runtime: supply.Manifest{Release: "1.0.0", Runtime: "fixture", Items: []supply.Item{vmm, kernel, tools}}, Image: image.Request{Ref: "registry.example/team/dev:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 300}, Agents: supply.ContentRef{Name: "agents", SHA256: strings.Repeat("e", 64), Size: 4096}}, Driver: "chv", CPUs: 2, DiskMax: 16 << 20}
}

func TestCanonicalNeedHasExactlyEightFieldsAndOneBootType(t *testing.T) {
	need := fixtureNeed()
	data, err := EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 8 {
		t.Fatal("Need JSON did not have exactly eight fields", len(fields), err)
	}
	got, err := DecodeV1(data, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got.Driver != "chv" || got.Image.RootHash != need.Image.RootHash || got.GuestRAM != need.GuestRAM {
		t.Fatal("the original client Need changed during codec roundtrip")
	}
	budget, err := BudgetFor(need.GuestRAM)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.ValidateWall("amd64", "microvm", "chv", budget.Limit); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		level, driver string
		cap           int64
	}{{"none", "chv", budget.Limit}, {"microvm", "process", budget.Limit}, {"microvm", "chv", budget.Guest}} {
		if got.ValidateWall("amd64", tc.level, tc.driver, tc.cap) == nil {
			t.Fatal("conflicting wall or unit limit accepted")
		}
	}
}

func TestCanonicalNeedRejectsAliasesDuplicatesUnknownAndTrailing(t *testing.T) {
	data, err := EncodeV1(fixtureNeed(), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for name, changed := range map[string]string{
		"version alias":    strings.Replace(text, `"version":`, `"Version":`, 1),
		"outer duplicate":  strings.Replace(text, `"version":1`, `"version":1,"version":1`, 1),
		"nested alias":     strings.Replace(text, `"root_hash":`, `"Root_Hash":`, 1),
		"nested duplicate": strings.Replace(text, `"sha256":`, `"sha256":"ignored","sha256":`, 1),
		"unknown security": strings.Replace(text, `"driver":"chv"`, `"driver":"chv","unvalidated_host_pin":true`, 1),
		"trailing":         text + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if plan, err := DecodeV1([]byte(changed), "amd64"); err == nil || plan != nil {
				t.Fatal("ambiguous or unknown Need became typed authority")
			}
		})
	}
}

func TestCanonicalNeedRejectsRolesArchitectureRootsAndLimits(t *testing.T) {
	for name, change := range map[string]func(*NeedV1){
		"arch":         func(n *NeedV1) { n.Image.Arch = "arm64" },
		"missing root": func(n *NeedV1) { n.Image.RootHash = ""; n.Image.DataBlocks = 0 },
		"unknown role": func(n *NeedV1) { n.Runtime.Items[0].Kind = "unknown" },
		"extra VMM": func(n *NeedV1) {
			extra := n.Runtime.Items[0]
			extra.Name = "other"
			n.Runtime.Items = append(n.Runtime.Items, extra)
		},
		"agent claim":      func(n *NeedV1) { n.Agents.Name = "vmm" },
		"unaligned agents": func(n *NeedV1) { n.Agents.Size++ },
		"oversized agents": func(n *NeedV1) { n.Agents.Size = image.MaxAgentsDisk + 4096 },
		"transfer":         func(n *NeedV1) { n.Image.Transfer = "host-transfer" },
		"RAM":              func(n *NeedV1) { n.GuestRAM++ },
		"CPU":              func(n *NeedV1) { n.CPUs = 0 },
		"pinned CPU bound": func(n *NeedV1) { n.CPUs = 8193 },
		"disk":             func(n *NeedV1) { n.DiskMax = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			need := fixtureNeed()
			change(&need)
			data, err := json.Marshal(need)
			if err != nil {
				t.Fatal(err)
			}
			if typed, err := DecodeV1(data, "amd64"); err == nil || typed != nil {
				t.Fatal("invalid Need was accepted before persistence")
			}
		})
	}
}

func TestUnknownFutureNeedIsOpaqueAndNeverInterpreted(t *testing.T) {
	data := []byte(" {\"version\":2,\"future_body\":{\"private_layout\":true}}\n")
	original := append([]byte(nil), data...)
	plan, err := DecodeV1(data, "amd64")
	var future *UnsupportedVersionError
	if plan != nil || !errors.As(err, &future) || future.Version != 2 || !bytes.Equal(future.Raw, original) {
		t.Fatal("future metadata was interpreted, lost or not typed")
	}
	data[1] = 'x'
	if !bytes.Equal(future.Raw, original) {
		t.Fatal("opaque preserved metadata aliases the caller's buffer")
	}
	for _, malformed := range []string{`{"Version":2}`, `{"version":2,"version":1}`, `{"version":2} {}`, `{"version":2.0}`} {
		if _, err := DecodeV1([]byte(malformed), "amd64"); err == nil || errors.As(err, &future) {
			t.Fatal("ambiguous future envelope was preserved as a usable version")
		}
	}
}
