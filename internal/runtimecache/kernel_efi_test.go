package runtimecache

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestSameArm64KernelBytesRetainDistinctSignedFormatRolesV1(t *testing.T) {
	base := supply.ReleaseFolder("1.2.3") + "/"
	guest := supply.NewItem(supply.KindKernel, "guest", "6.18.63", "arm64", []byte("same raw arm64 EFI Image bytes"), base+supply.KernelAsset("arm64"))
	guest.Licence, guest.SourceURL, guest.Released = "GPL-2.0-only", base+supply.KernelSourceAsset(guest.Version), "2026-11-01"
	efi := guest
	efi.Name, efi.URL = supply.KernelEFIName, base+supply.KernelEFIAsset("arm64")
	manifest := supply.Manifest{Release: "1.2.3", Runtime: "runtime-efi", Items: []supply.Item{guest, efi}}
	if err := Validate(manifest, "arm64"); err != nil {
		t.Fatal(err)
	}
	if Mode(guest) != 0o444 || Mode(efi) != 0o444 || guest.SHA256 != efi.SHA256 {
		t.Fatal("same bytes changed physical readonly role")
	}
	efi.Name = "guest"
	manifest.Items[1] = efi
	if err := Validate(manifest, "arm64"); err == nil {
		t.Fatal("different signed URL renamed to original guest role")
	}
}
