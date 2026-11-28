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
