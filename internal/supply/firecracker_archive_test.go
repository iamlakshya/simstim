package supply

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testFCELF(arch string) []byte {
	b := make([]byte, 64)
	copy(b, "\x7fELF\x02\x01\x01")
	binary.LittleEndian.PutUint16(b[16:], 2)
	machine := uint16(62)
	if arch == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(b[18:], machine)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	return b
}

func testFCArchive(t *testing.T, name string, kind byte, data []byte, duplicate bool) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for range 1 + btoi(duplicate) {
		h := &tar.Header{Name: name, Typeflag: kind, Mode: 0755, Size: int64(len(data))}
		if kind != tar.TypeReg {
			h.Size, data = 0, nil
			h.Linkname = "/outside"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func testPin(data []byte) LockFile {
	sum := sha256.Sum256(data)
	return LockFile{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

func TestFirecrackerMirrorHashesRawFileAndRequiresSource(t *testing.T) {
	for _, arch := range Arches {
		t.Run(arch, func(t *testing.T) {
			v := LockVMM{Name: FirecrackerName, Version: "1.18.0", Released: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), EOL: time.Date(2027, 5, 9, 0, 0, 0, 0, time.UTC)}
			asset, _ := VMMAssetName(v, arch)
			machine := "x86_64"
			if arch == "arm64" {
				machine = "aarch64"
			}
			raw := testFCELF(arch)
			archive := testFCArchive(t, "release-v1.18.0-"+machine+"/"+asset, tar.TypeReg, raw, false)
			v.AMD64, v.ARM64, v.Source = testPin(archive), testPin(archive), testPin([]byte("source"))
			inputs, output := t.TempDir(), t.TempDir()
			input, source := filepath.Join(inputs, "runtime.tgz"), filepath.Join(inputs, "source.tar.gz")
			for p, data := range map[string][]byte{input: archive, source: []byte("source")} {
				if err := os.WriteFile(p, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := MirrorVMM("0.6.0", v, arch, input, output); err == nil {
				t.Fatal("missing source passed")
			}
			if err := MirrorVMMSource(v, source, output); err != nil {
				t.Fatal(err)
			}
			it, err := MirrorVMM("0.6.0", v, arch, input, output)
			if err != nil {
				t.Fatal(err)
			}
			if it.SHA256 != testPin(raw).SHA256 || it.SHA256 == v.AMD64.SHA256 || it.Size != int64(len(raw)) || it.Licence != "Apache-2.0" || it.HostKernel != FirecrackerHostKernel {
				t.Fatalf("item does not name the raw runtime: %+v", it)
			}
			if err := CheckFile(it, filepath.Join(output, asset)); err != nil {
				t.Fatal(err)
			}
			if _, err := ReleaseVMMItem("0.6.0", v, arch); err == nil {
				t.Fatal("archive digest became runtime authority")
			}
			archive[len(archive)/2] ^= 1
			if err := os.WriteFile(input, archive, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := MirrorVMM("0.6.0", v, arch, input, output); err == nil {
				t.Fatal("tampered pinned archive passed")
			}
		})
	}
}

func TestFirecrackerArchiveRejectsInvalidRuntime(t *testing.T) {
	v := LockVMM{Name: FirecrackerName, Version: "1.18.0"}
	want := "release-v1.18.0-x86_64/firecracker-v1.18.0-x86_64"
	for _, tc := range []struct {
		name, member string
		kind         byte
		data         []byte
		duplicate    bool
	}{
		{"missing", "jailer-v1.18.0-x86_64", tar.TypeReg, testFCELF("amd64"), false},
		{"path traversal", "../../" + want, tar.TypeReg, testFCELF("amd64"), false},
		{"symlink", want, tar.TypeSymlink, nil, false},
		{"hardlink", want, tar.TypeLink, nil, false},
		{"duplicate", want, tar.TypeReg, testFCELF("amd64"), true},
		{"wrong architecture", want, tar.TypeReg, testFCELF("arm64"), false},
		{"text executable", want, tar.TypeReg, []byte("#!/bin/sh\n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := testFCArchive(t, tc.member, tc.kind, tc.data, tc.duplicate)
			if _, err := firecrackerExecutable(v, "amd64", bytes.NewReader(data)); err == nil {
				t.Fatal("invalid runtime passed")
			}
		})
	}
	data := testFCArchive(t, want, tar.TypeReg, testFCELF("amd64"), false)
	data[len(data)-8] ^= 1
	if _, err := firecrackerExecutable(v, "amd64", bytes.NewReader(data)); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt gzip trailer: %v", err)
	}
}

// This opt-in check reads public pinned release inputs. It never executes
// downloaded content and needs neither Linux nor a hypervisor.
func TestFirecrackerPublicReleaseInputs(t *testing.T) {
	dir := os.Getenv("FIRECRACKER_INPUTS_DIR")
	if dir == "" {
		t.Skip("set FIRECRACKER_INPUTS_DIR to verified v1.18.0 archives and source")
	}
	lock, err := LoadLockFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	var v LockVMM
	for _, candidate := range lock.VMMs {
		if candidate.Name == FirecrackerName {
			v = candidate
		}
	}
	out := t.TempDir()
	source, _ := VMMInputName(v, "source")
	if err := MirrorVMMSource(v, filepath.Join(dir, source), out); err != nil {
		t.Fatal(err)
	}
	for arch, expected := range map[string]struct {
		size int64
		sha  string
	}{
		"amd64": {3921872, "b33f46cd6d88834cf6a4f8d184f08f219809c636236bffd286d233a60602a2ba"},
		"arm64": {3623592, "6f9e08df0a0072e3e7086c51ff434c034d705845e349175a01beb3f9cc025ce3"},
	} {
		input, _ := VMMInputName(v, arch)
		it, err := MirrorVMM("0.6.0", v, arch, filepath.Join(dir, input), out)
		if err != nil || it.SHA256 != expected.sha || it.Size != expected.size {
			t.Fatalf("%s mirror: %+v, %v", arch, it, err)
		}
	}
}
