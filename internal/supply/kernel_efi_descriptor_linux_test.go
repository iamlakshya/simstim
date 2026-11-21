//go:build linux

package supply

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFinalEFIKernelCheckUsesOnlyInheritedMeasuredOrSealedFormatV1(t *testing.T) {
	for _, valid := range []bool{true, false} {
		data := kernelEFIFormatFixtureV1(runtime.GOARCH)
		if !valid {
			data[0] = 0
		}
		base := ReleaseFolder("1.2.3") + "/"
		item := NewItem(KindKernel, KernelEFIName, "6.18.63", runtime.GOARCH, data, base+KernelEFIAsset(runtime.GOARCH))
		item.Licence, item.SourceURL, item.Released = "GPL-2.0-only", base+KernelSourceAsset(item.Version), "2026-11-01"
		var err error
		item.FSVerity, err = FSVerityDigest(t.Context(), bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "original-efi-kernel")
		if err := os.WriteFile(path, data, 0o444); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		measured := verityCalls{measure: func(actual *os.File) (string, error) {
			if actual != file {
				t.Fatal("final EFI check substituted its inherited descriptor")
			}
			return item.FSVerity, nil
		}}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := checkRuntimeDescriptor(t.Context(), item, file, measured); (err == nil) != valid {
			file.Close()
			t.Fatal("final immutable measured format check changed", valid, err)
		}
		file.Close()
		if !valid {
			continue
		}
		if err := os.WriteFile(path, data, 0o444); err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := SealRuntime(t.Context(), item, source)
		source.Close()
		if err != nil {
			t.Fatal(err)
		}
		absent := verityCalls{measure: func(*os.File) (string, error) { return "", unix.ENODATA }}
		if err := checkRuntimeDescriptor(t.Context(), item, sealed, absent); err != nil {
			sealed.Close()
			t.Fatal(err)
		}
		sealed.Close()
	}
}
