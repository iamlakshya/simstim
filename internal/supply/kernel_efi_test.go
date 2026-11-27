package supply

import (
	"encoding/binary"
	"testing"
)

// Synthetic format fixture only; these bytes do not prove a kernel can boot.
func kernelEFIFormatFixtureV1(arch string) []byte {
	data := make([]byte, 1024)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[0x3c:], 0x80)
	copy(data[0x80:], "PE\x00\x00")
	header := data[0x84:]
	machine := uint16(0x8664)
	if arch == "arm64" {
		machine = 0xaa64
		copy(data[0x38:], "ARMd")
		binary.LittleEndian.PutUint64(data[0x10:], 4096)
	} else {
		binary.LittleEndian.PutUint16(data[0x1fe:], 0xaa55)
		copy(data[0x202:], "HdrS")
		binary.LittleEndian.PutUint16(data[0x206:], 0x20b)
		data[0x211] = 1
	}
	binary.LittleEndian.PutUint16(header, machine)
	binary.LittleEndian.PutUint16(header[2:], 1)
	binary.LittleEndian.PutUint16(header[16:], 112)
	binary.LittleEndian.PutUint16(header[18:], 2)
	optional := data[0x98:]
	binary.LittleEndian.PutUint16(optional, 0x20b)
	binary.LittleEndian.PutUint32(optional[16:], 0x1000)
	binary.LittleEndian.PutUint32(optional[32:], 4096)
	binary.LittleEndian.PutUint32(optional[36:], 512)
	binary.LittleEndian.PutUint32(optional[56:], 8192)
	binary.LittleEndian.PutUint32(optional[60:], 512)
	binary.LittleEndian.PutUint16(optional[68:], 10)
	section := data[0x98+112:]
	copy(section, ".text")
	binary.LittleEndian.PutUint32(section[8:], 512)
	binary.LittleEndian.PutUint32(section[12:], 0x1000)
	binary.LittleEndian.PutUint32(section[16:], 512)
	binary.LittleEndian.PutUint32(section[20:], 512)
	binary.LittleEndian.PutUint32(section[36:], 0x60000020)
	return data
}

func TestKernelEFIChecksActualArchitectureLinuxFormatAndEntryV1(t *testing.T) {
	for _, arch := range Arches {
		data := kernelEFIFormatFixtureV1(arch)
		if err := CheckKernelEFI(arch, data); err != nil {
			t.Fatal(arch, err)
		}
		for _, mutate := range []func([]byte){
			func(data []byte) { data[0] = 0 },
			func(data []byte) { binary.LittleEndian.PutUint16(data[0x84:], 0x14c) },
			func(data []byte) { binary.LittleEndian.PutUint16(data[0x98+68:], 3) },
			func(data []byte) { binary.LittleEndian.PutUint32(data[0x98+16:], 0x3000) },
			func(data []byte) { binary.LittleEndian.PutUint32(data[0x98+112+36:], 0x40000040) },
			func(data []byte) {
				if arch == "amd64" {
					data[0x202] = 0
				} else {
					data[0x38] = 0
				}
			},
		} {
			wrong := append([]byte(nil), data...)
			mutate(wrong)
			if err := CheckKernelEFI(arch, wrong); err == nil {
				t.Fatal("malformed or foreign EFI kernel accepted", arch)
			}
		}
	}
	if err := CheckKernelEFI("amd64", []byte("\x7fELF")); err == nil {
		t.Fatal("PVH ELF substituted for EFI bzImage")
	}
}

func TestKernelEFIRoleCannotAliasGuestURLOrBypassSourceAdvisoriesV1(t *testing.T) {
	base := ReleaseFolder("1.2.3") + "/"
	item := NewItem(KindKernel, KernelEFIName, "6.18.63", "amd64", kernelEFIFormatFixtureV1("amd64"), base+KernelEFIAsset("amd64"))
	item.Licence, item.SourceURL, item.Released = "GPL-2.0-only", base+KernelSourceAsset(item.Version), "2026-11-01"
	manifest := &Manifest{Release: "1.2.3", Items: []Item{item}}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	wrong := *manifest
	wrong.Items = append([]Item(nil), manifest.Items...)
	wrong.Items[0].URL = base + KernelAsset("amd64")
	if err := wrong.Validate(); err == nil {
		t.Fatal("EFI role adopted original guest tuple")
	}
	manifest.Advisories = []Advisory{{ID: "GHSA-2345-2345-2345", Kind: AdvisoryKernel, Name: "guest", Before: "6.18.64", Severity: "high", CVSS: 8}}
	if found := manifest.OwnFindings(); len(found) != 1 || found[0].Item.Name != KernelEFIName {
		t.Fatal("EFI delivery role bypassed same-source kernel advisory", found)
	}
	if _, found := manifest.RuntimeFinding([]string{item.SHA256}); !found {
		t.Fatal("runtime EFI digest bypassed source advisory")
	}
}
