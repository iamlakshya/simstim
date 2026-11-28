package helpers

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestDigestsFromEmbedded(t *testing.T) {
	if !Embedded() {
		t.Skip("this build has no embedded helpers")
	}
	digests := Digests()
	if len(digests) == 0 {
		t.Fatal("Digests() is empty with embedded helpers")
	}
	for _, d := range digests {
		raw, ok := For(d.OS, d.Arch)
		if !ok {
			t.Fatalf("For(%q, %q) = false, want true", d.OS, d.Arch)
		}
		want := sha256Hex(raw)
		if d.SHA256 != want {
			t.Fatalf("Digests()[%s/%s] = %s, want %s", d.OS, d.Arch, d.SHA256, want)
		}
		got, ok := DigestFor(d.OS, d.Arch)
		if !ok || got != want {
			t.Fatalf("DigestFor(%q, %q) = (%q, %v), want (%q, true)", d.OS, d.Arch, got, ok, want)
		}
	}
}

func TestLoadCachedAndDigestsFromCache(t *testing.T) {
	resetFetched(t)

	if got := Digests(); len(got) != 0 {
		t.Fatalf("Digests() with no helpers = %+v, want empty", got)
	}
	if _, ok := DigestFor("linux", "amd64"); ok {
		t.Fatal("DigestFor with no helpers = true, want false")
	}

	cacheDir := t.TempDir()
	const ver = "0.9.0-test"
	if LoadCached(ver, cacheDir) {
		t.Fatal("LoadCached on empty cache = true, want false")
	}

	helperDir := filepath.Join(cacheDir, "helpers", ver)
	if err := os.MkdirAll(helperDir, 0o700); err != nil {
		t.Fatal(err)
	}
	amd64Bytes := []byte("fake-host-linux-amd64")
	arm64Bytes := []byte("fake-host-linux-arm64")
	if err := os.WriteFile(filepath.Join(helperDir, "host-linux-amd64"), amd64Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helperDir, "host-linux-arm64"), arm64Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helperDir, "vt-linux-amd64"), []byte("fake-vt"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !LoadCached(ver, cacheDir) {
		t.Fatal("LoadCached = false, want true")
	}
	digests := Digests()
	if len(digests) != 2 {
		t.Fatalf("Digests() = %+v, want 2 host helpers", digests)
	}
	wantAMD64 := sha256Hex(amd64Bytes)
	wantARM64 := sha256Hex(arm64Bytes)
	if digests[0] != (HelperDigest{OS: "linux", Arch: "amd64", SHA256: wantAMD64}) {
		t.Fatalf("digests[0] = %+v, want linux/amd64 %s", digests[0], wantAMD64)
	}
	if digests[1] != (HelperDigest{OS: "linux", Arch: "arm64", SHA256: wantARM64}) {
		t.Fatalf("digests[1] = %+v, want linux/arm64 %s", digests[1], wantARM64)
	}
	if got, ok := DigestFor("linux", "amd64"); !ok || got != wantAMD64 {
		t.Fatalf("DigestFor(linux, amd64) = (%q, %v), want (%q, true)", got, ok, wantAMD64)
	}
	if _, ok := DigestFor("darwin", "arm64"); ok {
		t.Fatal("DigestFor(darwin, arm64) = true, want false")
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
