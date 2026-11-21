package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func gvisorReleaseFixtureV1(t *testing.T) (string, string, gvisorSourcePinsV1) {
	t.Helper()
	repository := t.TempDir()
	patch := []byte("public fixture source patch\n")
	digest := sha256.Sum256(patch)
	pins := gvisorSourcePinsV1{upstream: strings.Repeat("a", 40), patch: hex.EncodeToString(digest[:]), released: "2026-11-02"}
	pins.version = "release-20261102.0-" + pins.patch[:8]
	files := map[string][]byte{
		"third_party/gvisor/hostguard.patch":       patch,
		"third_party/gvisor/hostguard-inputs.json": []byte(`{"upstream_commit":"` + pins.upstream + `","files":[]}`),
		"internal/wall/gvisor/native_build.go":     []byte("package gvisor\nconst NativePatchSHA256V1 = \"" + pins.patch + "\"\nconst NativeBuildVersionV1 = \"" + pins.version + "\"\n"),
	}
	for name, data := range files {
		path := filepath.Join(repository, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGvisorFixtureToolPinsV1(t, repository, pins)
	input := filepath.Join(repository, "native-originals")
	for _, arch := range supply.Arches {
		dir := filepath.Join(input, arch)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		record := gvisorBuildRecordV1{Upstream: pins.upstream, Patch: pins.patch, Version: pins.version, Arch: arch}
		for _, name := range gvisorReleaseRolesV1 {
			data := gvisorFixtureELFV1(arch)
			item := supply.NewItem(supply.KindGvisor, name, pins.version, arch, data, "")
			record.Items = append(record.Items, gvisorBuildItemV1{Kind: item.Kind, Name: name, Version: pins.version, Arch: arch, Released: pins.released, SHA256: item.SHA256, Size: item.Size})
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o555); err != nil {
				t.Fatal(err)
			}
		}
		writeGvisorFixtureProvenanceV1(t, repository, dir, &record)
		writeGvisorRecordV1(t, filepath.Join(dir, "build.json"), record)
	}
	return repository, input, pins
}

// These synthetic bytes test ELF validation and manifest binding only.
// They are not executable native-role or memory-guard acceptance fixtures.
func gvisorFixtureELFV1(arch string) []byte {
	data := make([]byte, 1024)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	order := binary.LittleEndian
	order.PutUint16(data[16:], 2)
	machine := uint16(62)
	if arch == "arm64" {
		machine = 183
	}
	order.PutUint16(data[18:], machine)
	order.PutUint32(data[20:], 1)
	order.PutUint64(data[24:], 0x400100)
	order.PutUint64(data[32:], 64)
	order.PutUint64(data[40:], 512)
	order.PutUint16(data[52:], 64)
	order.PutUint16(data[54:], 56)
	order.PutUint16(data[56:], 1)
	order.PutUint16(data[58:], 64)
	order.PutUint16(data[60:], 4)
	program := data[64:120]
	order.PutUint32(program, 1)
	order.PutUint32(program[4:], 5)
	order.PutUint64(program[16:], 0x400000)
	order.PutUint64(program[32:], uint64(len(data)))
	order.PutUint64(program[40:], uint64(len(data)))
	order.PutUint64(program[48:], 4096)
	symbols := data[512+64 : 512+128]
	order.PutUint32(symbols[4:], 2)
	order.PutUint64(symbols[24:], 768)
	order.PutUint64(symbols[32:], 48)
	order.PutUint32(symbols[40:], 2)
	order.PutUint64(symbols[56:], 24)
	name := []byte("\x00gvisor.dev/gvisor/runsc/hostguard.entry.abi0\x00")
	stringsSection := data[512+128 : 512+192]
	order.PutUint32(stringsSection[4:], 3)
	order.PutUint64(stringsSection[24:], 816)
	order.PutUint64(stringsSection[32:], uint64(len(name)))
	text := data[512+192 : 512+256]
	order.PutUint32(text[4:], 1)
	order.PutUint64(text[8:], 6)
	order.PutUint64(text[16:], 0x400100)
	order.PutUint64(text[24:], 256)
	order.PutUint64(text[32:], 1)
	symbol := data[768+24 : 768+48]
	order.PutUint32(symbol, 1)
	symbol[4] = 0x12
	order.PutUint16(symbol[6:], 3)
	order.PutUint64(symbol[8:], 0x400100)
	order.PutUint64(symbol[16:], 1)
	copy(data[816:], name)
	return data
}

func writeGvisorRecordV1(t *testing.T, path string, record gvisorBuildRecordV1) {
	t.Helper()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGvisorSignedItemsBindAllTenActualFilesAndCurrentPatchV1(t *testing.T) {
	repository, input, pins := gvisorReleaseFixtureV1(t)
	t.Chdir(repository)
	manifest := &supply.Manifest{Release: "1.2.3"}
	if err := addGvisorItemsV1(manifest, input, true); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Items) != 10 || manifest.Runtime == "" {
		t.Fatal("partial native closure")
	}
	for _, item := range manifest.Items {
		if item.Version != pins.version || item.Released != pins.released || item.Licence != "Apache-2.0" {
			t.Fatal("source pin replaced by a build label", item)
		}
		if err := supply.CheckFile(item, filepath.Join(repository, "build/gvisor/assets", item.Name+"-"+item.Arch)); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkGvisorReleaseItemsV1(manifest, input, true); err != nil {
		t.Fatal(err)
	}
	manifest.Items[9].SHA256 = strings.Repeat("b", 64)
	if err := checkGvisorReleaseItemsV1(manifest, input, true); err == nil {
		t.Fatal("changed uploaded companion passed")
	}
}

func TestGvisorBuildRecordCannotSupplyOldMissingOrForeignRolesV1(t *testing.T) {
	_, input, pins := gvisorReleaseFixtureV1(t)
	raw, err := os.ReadFile(filepath.Join(input, "amd64/build.json"))
	if err != nil {
		t.Fatal(err)
	}
	base, err := decodeGvisorBuildV1(raw, pins, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*gvisorBuildRecordV1){
		func(record *gvisorBuildRecordV1) { record.Version = "release-20261102.0-dea09068" },
		func(record *gvisorBuildRecordV1) { record.Patch = strings.Repeat("c", 64) },
		func(record *gvisorBuildRecordV1) { record.Arch = "arm64" },
		func(record *gvisorBuildRecordV1) { record.Items = record.Items[:4] },
		func(record *gvisorBuildRecordV1) { record.Items[4].Name = "../runsc" },
		func(record *gvisorBuildRecordV1) { record.Items[4].Version = "release-20261102.0-dea09068" },
	} {
		record := base
		record.Items = append([]gvisorBuildItemV1(nil), base.Items...)
		mutate(&record)
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeGvisorBuildV1(append(data, '\n'), pins, "amd64"); err == nil {
			t.Fatal("foreign or partial native build accepted")
		}
	}
	duplicate := strings.Replace(string(raw), `"arch": "amd64",`, `"arch": "arm64", "arch": "amd64",`, 1)
	if _, err := decodeGvisorBuildV1([]byte(duplicate), pins, "amd64"); err == nil {
		t.Fatal("duplicate native metadata accepted")
	}
}

func TestGvisorDeliveryRejectsReplacedBytesAndEntryWithoutPartialManifestV1(t *testing.T) {
	for _, change := range []string{"missing-arm", "bytes", "interpreter", "entry", "source-pin"} {
		t.Run(change, func(t *testing.T) {
			repository, input, _ := gvisorReleaseFixtureV1(t)
			t.Chdir(repository)
			path := filepath.Join(input, "amd64/runsc")
			switch change {
			case "missing-arm":
				if err := os.Remove(filepath.Join(input, "arm64/build.json")); err != nil {
					t.Fatal(err)
				}
			case "source-pin":
				if err := os.WriteFile(filepath.Join(repository, "third_party/gvisor/hostguard.patch"), []byte("another patch\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if change == "bytes" {
					data[900]++
				} else if change == "entry" {
					binary.LittleEndian.PutUint64(data[24:], 0x400101)
				} else {
					binary.LittleEndian.PutUint16(data[56:], 2)
					binary.LittleEndian.PutUint32(data[120:], 3)
				}
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o555); err != nil {
					t.Fatal(err)
				}
				// Updating an unsigned record cannot make a malformed actual ELF
				// acquire the guarded-entry or no-interpreter property.
				if change != "bytes" {
					raw, err := os.ReadFile(filepath.Join(input, "amd64/build.json"))
					if err != nil {
						t.Fatal(err)
					}
					var record gvisorBuildRecordV1
					if err := json.Unmarshal(raw, &record); err != nil {
						t.Fatal(err)
					}
					digest := sha256.Sum256(data)
					record.Items[0].SHA256 = hex.EncodeToString(digest[:])
					writeGvisorFixtureProvenanceV1(t, repository, filepath.Dir(path), &record)
					writeGvisorRecordV1(t, filepath.Join(input, "amd64/build.json"), record)
				}
			}
			manifest := &supply.Manifest{Release: "1.2.3"}
			if err := addGvisorItemsV1(manifest, input, true); err == nil || len(manifest.Items) != 0 {
				t.Fatal("bad native closure published partial metadata", err)
			}
		})
	}
}
