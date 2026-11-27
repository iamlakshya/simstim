package supply

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"fmt"
	"io"
)

// Bounds apply after the archive's exact size and SHA-256 have passed. Only
// one regular executable is selected; no archive path is used as a disk path.
const maxFirecrackerExpanded = 128 << 20
const maxFirecrackerExecutable = 64 << 20

func firecrackerExecutable(v LockVMM, arch string, input io.Reader) (*bytes.Reader, error) {
	name, err := VMMAssetName(v, arch)
	if err != nil {
		return nil, err
	}
	machine := "x86_64"
	if arch == "arm64" {
		machine = "aarch64"
	}
	want := "release-v" + v.Version + "-" + machine + "/" + name
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, fmt.Errorf("open Firecracker archive: %w", err)
	}
	defer gz.Close()
	bounded := &io.LimitedReader{R: gz, N: maxFirecrackerExpanded + 1}
	tr := tar.NewReader(bounded)
	var data []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Firecracker archive: %w", err)
		}
		if h.Name != want {
			continue
		}
		if data != nil || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > maxFirecrackerExecutable {
			return nil, fmt.Errorf("Firecracker archive member %q must be one bounded regular executable", want)
		}
		data, err = io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read Firecracker executable: %w", err)
		}
	}
	// Read through the gzip trailer as well; tar's end marker alone does not
	// prove the compressed stream's checksum or its expanded size.
	if _, err := io.Copy(io.Discard, bounded); err != nil {
		return nil, fmt.Errorf("check Firecracker gzip trailer: %w", err)
	}
	if bounded.N == 0 {
		return nil, fmt.Errorf("Firecracker archive exceeds %d expanded bytes", maxFirecrackerExpanded)
	}
	if data == nil {
		return nil, fmt.Errorf("Firecracker archive has no runtime member %q", want)
	}
	if err := checkFirecrackerELF(data, arch); err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func checkFirecrackerELF(data []byte, arch string) error {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("read Firecracker ELF: %w", err)
	}
	defer f.Close()
	want := elf.EM_X86_64
	if arch == "arm64" {
		want = elf.EM_AARCH64
	}
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != want || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return fmt.Errorf("Firecracker runtime is not a 64-bit little-endian %s executable", arch)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return fmt.Errorf("Firecracker runtime needs a dynamic interpreter; use upstream's static musl release")
		}
	}
	return nil
}
