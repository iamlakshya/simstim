package supply

import (
	"math"
	"strings"
	"testing"
)

func TestBootItemsRejectWrongAssetsAndVerityStructure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"kernel licence missing", func(m *Manifest) { m.Items[3].Licence = "" }},
		{"kernel licence wrong", func(m *Manifest) { m.Items[3].Licence = "MIT" }},
		{"kernel asset for another architecture", func(m *Manifest) { m.Items[3].URL = base + "vmlinux-amd64" }},
		{"kernel source for another version", func(m *Manifest) { m.Items[3].SourceURL = base + "linux-6.17.tar.xz" }},
		{"tools asset for another architecture", func(m *Manifest) { m.Items[4].URL = base + "tools-arm64.erofs" }},
		{"tools nonzero salt", func(m *Manifest) { m.Items[4].Salt = strings.Repeat("1", 64) }},
		{"tools partial block", func(m *Manifest) { m.Items[4].Size = 8193 }},
		{"tools data exceeds file", func(m *Manifest) { m.Items[4].DataBlocks = 3 }},
		{"tools has no tree", func(m *Manifest) { m.Items[4].DataBlocks = 2 }},
		{"tools data byte overflow", func(m *Manifest) { m.Items[4].DataBlocks = math.MaxInt64/4096 + 1 }},
		{"image data byte overflow", func(m *Manifest) { m.Items[5].DataBlocks = math.MaxInt64/4096 + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sample()
			tc.change(m)
			if _, err := Load(raw(t, m)); err == nil {
				t.Fatal("Load accepted the invalid boot item")
			}
			if _, err := Marshal(m); err == nil {
				t.Fatal("Marshal accepted the invalid boot item")
			}
		})
	}
}

func TestBootItemsKeepValidArchitectureAndVerityBounds(t *testing.T) {
	for _, arch := range Arches {
		m := sample()
		m.Items[3].Arch, m.Items[3].URL = arch, base+KernelAsset(arch)
		m.Items[4].Arch, m.Items[4].URL = arch, base+ToolsAsset(arch)
		if _, err := Load(raw(t, m)); err != nil {
			t.Fatalf("valid %s boot pair: %v", arch, err)
		}
	}
	m := sample()
	m.Items[5].DataBlocks = math.MaxInt64 / 4096
	if _, err := Load(raw(t, m)); err != nil {
		t.Fatalf("largest representable image data region: %v", err)
	}
}
