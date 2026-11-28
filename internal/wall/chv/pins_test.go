package chv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestPinsAndDownloadVerification(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		p, err := PinsForArch(arch)
		if err != nil {
			t.Fatalf("PinsForArch(%s): %v", arch, err)
		}
		if !isHex64(p.VMM.SHA256) || !isHex64(p.CHRemote.SHA256) || !isHex64(p.Kernel.Archive.SHA256) || !isHex64(p.Kernel.KernelSHA256) {
			t.Fatalf("invalid SHA-256 pins for %s: %+v", arch, p)
		}
	}
	if _, err := PinsForArch("mips"); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("expected error with Try hint for unsupported arch, got %v", err)
	}

	payload := []byte("fake-static-binary-bytes")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "cloud-hypervisor")

	// Matching SHA-256 downloads, verifies, and installs atomically.
	err := ensurePinnedBinary(context.Background(), srv.Client(), dst, FilePin{
		URL:    srv.URL + "/chv",
		SHA256: hexSum,
		Size:   int64(len(payload)),
	})
	if err != nil {
		t.Fatalf("ensurePinnedBinary valid: %v", err)
	}
	if err := VerifyFileSHA256(dst, hexSum); err != nil {
		t.Fatalf("VerifyFileSHA256: %v", err)
	}

	// Wrong SHA-256 fails closed and leaves no file.
	badDst := filepath.Join(dir, "bad-vmm")
	err = ensurePinnedBinary(context.Background(), srv.Client(), badDst, FilePin{
		URL:    srv.URL + "/chv",
		SHA256: strings.Repeat("0", 64),
		Size:   int64(len(payload)),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("expected SHA-256 mismatch error with Try hint, got %v", err)
	}
	if _, statErr := os.Stat(badDst); !os.IsNotExist(statErr) {
		t.Fatalf("corrupted download must not be installed at %s", badDst)
	}
}

func TestPinsMatchSupplyLock(t *testing.T) {
	lock, err := supply.LoadLock(filepath.Join("..", "..", "..", "guest", "supply.lock"))
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if lock.Kernel.KataVersion != PinnedKataVersion || lock.Kernel.Version != PinnedKernelVersion {
		t.Fatalf("kernel version drift: lock=%s/%s chv=%s/%s",
			lock.Kernel.KataVersion, lock.Kernel.Version, PinnedKataVersion, PinnedKernelVersion)
	}
	vmm, err := lock.FindVMM("cloud-hypervisor")
	if err != nil {
		t.Fatalf("FindVMM: %v", err)
	}
	if vmm.Version != PinnedVMMVersion || vmm.AdvisoryFloor != AdvisoryFloor {
		t.Fatalf("vmm version drift: lock=%s/%s chv=%s/%s",
			vmm.Version, vmm.AdvisoryFloor, PinnedVMMVersion, AdvisoryFloor)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		p, err := PinsForArch(arch)
		if err != nil {
			t.Fatalf("PinsForArch(%s): %v", arch, err)
		}
		ka, err := lock.Kernel.Arch(arch)
		if err != nil {
			t.Fatalf("Kernel.Arch(%s): %v", arch, err)
		}
		if p.Kernel.Archive.URL != ka.URL ||
			p.Kernel.Archive.SHA256 != ka.ArchiveSHA256 ||
			p.Kernel.Archive.Size != ka.ArchiveSize ||
			p.Kernel.ArchiveMember != lock.Kernel.ArchiveMember ||
			p.Kernel.ConfigMember != lock.Kernel.ConfigMember ||
			p.Kernel.KernelSHA256 != ka.SHA256 ||
			p.Kernel.KernelSize != ka.Size ||
			p.Kernel.ConfigSHA256 != ka.ConfigSHA256 ||
			p.Kernel.ConfigSize != ka.ConfigSize {
			t.Errorf("%s kernel pin drift between chv.Pins and guest/supply.lock", arch)
		}
		vPin, rPin, err := vmm.BinaryFor(arch)
		if err != nil {
			t.Fatalf("VMM.BinaryFor(%s): %v", arch, err)
		}
		if p.VMM.URL != vPin.URL || p.VMM.SHA256 != vPin.SHA256 || p.VMM.Size != vPin.Size {
			t.Errorf("%s VMM pin drift between chv.Pins and guest/supply.lock", arch)
		}
		if p.CHRemote.URL != rPin.URL || p.CHRemote.SHA256 != rPin.SHA256 || p.CHRemote.Size != rPin.Size {
			t.Errorf("%s ch-remote pin drift between chv.Pins and guest/supply.lock", arch)
		}
	}
}
