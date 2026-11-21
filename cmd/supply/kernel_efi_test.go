package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

// Synthetic Linux EFI headers exercise the producer; they are not boot proof.
func kernelEFIProducerFixtureV1(arch string) []byte {
	data := make([]byte, 1024)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[0x3c:], 0x80)
	copy(data[0x80:], "PE\x00\x00")
	header, optional, section := data[0x84:], data[0x98:], data[0x108:]
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
	binary.LittleEndian.PutUint16(optional, 0x20b)
	binary.LittleEndian.PutUint32(optional[16:], 4096)
	binary.LittleEndian.PutUint32(optional[32:], 4096)
	binary.LittleEndian.PutUint32(optional[36:], 512)
	binary.LittleEndian.PutUint32(optional[56:], 8192)
	binary.LittleEndian.PutUint32(optional[60:], 512)
	binary.LittleEndian.PutUint16(optional[68:], 10)
	copy(section, ".text")
	binary.LittleEndian.PutUint32(section[8:], 512)
	binary.LittleEndian.PutUint32(section[12:], 4096)
	binary.LittleEndian.PutUint32(section[16:], 512)
	binary.LittleEndian.PutUint32(section[20:], 512)
	binary.LittleEndian.PutUint32(section[36:], 0x60000020)
	return data
}

func TestActualEFIKernelProducerKeepsBothSignedRolesAndVerityV1(t *testing.T) {
	lock, configs, guest := writeTestLockAndGuest(t)
	manifest := &supply.Manifest{Release: "0.6.0"}
	if err := addGuestItems(manifest, guest, lock, configs, true); err != nil {
		t.Fatal(err)
	}
	if err := ensureKernelEFIPresentV1(manifest); err == nil {
		t.Fatal("existing guest pair substituted for missing EFI kernels")
	}
	for _, arch := range supply.Arches {
		if err := os.WriteFile(filepath.Join(guest, "assets", supply.KernelEFIAsset(arch)), kernelEFIProducerFixtureV1(arch), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest.Items = nil
	if err := addGuestItems(manifest, guest, lock, configs, true); err != nil {
		t.Fatal(err)
	}
	if err := ensureKernelEFIPresentV1(manifest); err != nil {
		t.Fatal(err)
	}
	for _, arch := range supply.Arches {
		regular, ok := manifest.Find(supply.KindKernel, "guest", arch)
		if !ok {
			t.Fatal("original guest tuple disappeared")
		}
		efi, ok := manifest.Find(supply.KindKernel, supply.KernelEFIName, arch)
		if !ok {
			t.Fatal("EFI tuple missing")
		}
		if regular.Name == efi.Name || regular.URL == efi.URL || regular.SourceURL != efi.SourceURL || regular.Version != efi.Version || regular.Released != efi.Released {
			t.Fatal("kernel formats lost their exact shared-source/distinct-artifact contract")
		}
		for _, item := range []supply.Item{regular, efi} {
			asset := supply.KernelAsset(arch)
			if item.Name == supply.KernelEFIName {
				asset = supply.KernelEFIAsset(arch)
			}
			data, err := os.ReadFile(filepath.Join(guest, "assets", asset))
			if err != nil {
				t.Fatal(err)
			}
			verity, err := supply.FSVerityDigest(context.Background(), bytes.NewReader(data), int64(len(data)))
			if err != nil || item.FSVerity != verity {
				t.Fatal("kernel verity was not derived from actual output bytes", err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(guest, "assets", supply.KernelEFIAsset("amd64")), []byte("\x7fELF vmlinux is not an EFI bzImage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addGuestItems(&supply.Manifest{Release: "0.6.0"}, guest, lock, configs, true); err == nil {
		t.Fatal("mislabeled ELF kernel became signed EFI item")
	}
}
