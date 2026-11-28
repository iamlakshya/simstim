package supply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestRepoSupplyLockIsValid(t *testing.T) {
	lock, err := LoadLock(filepath.Join("..", "..", "guest", "supply.lock"))
	if err != nil {
		t.Fatalf("LoadLock(guest/supply.lock): %v", err)
	}
	if lock.Kernel.Version != "6.18.62" || lock.Kernel.KataVersion != "4.4.0" {
		t.Fatalf("unexpected kernel spec: %+v", lock.Kernel)
	}
	if len(lock.VMMs) != 1 || lock.VMMs[0].Name != "cloud-hypervisor" || lock.VMMs[0].Version != "55.0" {
		t.Fatalf("unexpected VMM specs: %+v", lock.VMMs)
	}
	if lock.VMMs[0].EOLString() != "2027-02-11" || lock.VMMs[0].ReleasedString() != "2026-11-19" {
		t.Fatalf("unexpected VMM dates: released=%q eol=%q", lock.VMMs[0].ReleasedString(), lock.VMMs[0].EOLString())
	}
}

func TestCheckLockRejectsMismatchedPinsAndBadFields(t *testing.T) {
	base, err := LoadLock(filepath.Join("..", "..", "guest", "supply.lock"))
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}

	t.Run("kernel kata_version disagrees with pin ref", func(t *testing.T) {
		bad := base
		bad.Kernel.KataVersion = "4.5.0"
		if err := CheckLock(bad); err == nil || !strings.Contains(err.Error(), "disagrees") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("err = %v, want disagreement with Try:", err)
		}
	})

	t.Run("vmm version disagrees with pin ref", func(t *testing.T) {
		bad := base
		bad.VMMs = append([]VMMSpec(nil), base.VMMs...)
		bad.VMMs[0].Version = "56.0"
		if err := CheckLock(bad); err == nil || !strings.Contains(err.Error(), "disagrees") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("err = %v, want disagreement with Try:", err)
		}
	})

	t.Run("missing pin for vmm", func(t *testing.T) {
		bad := base
		bad.Pins = []Pin{base.Pins[0], base.Pins[2]} // drop cloud-hypervisor pin
		if err := CheckLock(bad); err == nil || !strings.Contains(err.Error(), "no matching [[pin]]") {
			t.Fatalf("err = %v, want missing pin", err)
		}
	})

	t.Run("non-HTTPS url rejected", func(t *testing.T) {
		bad := base
		bad.Kernel.AMD64.URL = "http://example.invalid/kata.tar.zst"
		if err := CheckLock(bad); err == nil || !strings.Contains(err.Error(), "HTTPS") {
			t.Fatalf("err = %v, want HTTPS error", err)
		}
	})

	t.Run("unknown TOML field rejected", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "guest", "supply.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseLock(append(raw, []byte("\nunknown_field = 1\n")...)); err == nil {
			t.Fatal("expected unknown field to fail strict TOML decode")
		}
	})
}

func TestAppendAndVerifyRawVerityAndTamperDetection(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "tools-amd64.erofs")
	// Unaligned size exercises zero-padding to 4096-byte multiple.
	raw := bytes.Repeat([]byte("TOOLS-DATA-BLOCK"), 700) // 11200 bytes -> pads to 12288 (3 blocks)
	if err := os.WriteFile(img, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := AppendRawVerity(img)
	if err != nil {
		t.Fatalf("AppendRawVerity: %v", err)
	}
	if info.DataBlocks != 3 || info.Salt != ZeroSaltHex || len(info.RootHash) != 64 || len(info.SHA256) != 64 {
		t.Fatalf("unexpected VerityInfo: %+v", info)
	}

	got, err := VerifyRawVerity(img, info.DataBlocks, info.RootHash)
	if err != nil {
		t.Fatalf("VerifyRawVerity: %v", err)
	}
	if got != info {
		t.Fatalf("VerifyRawVerity = %+v, want %+v", got, info)
	}

	// Determinism: identical input produces byte-for-byte identical output.
	img2 := filepath.Join(dir, "tools-amd64-2.erofs")
	if err := os.WriteFile(img2, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	info2, err := AppendRawVerity(img2)
	if err != nil || info2 != info {
		t.Fatalf("second build = (%+v, %v), want %+v", info2, err, info)
	}

	// Flip 1 bit in data block 0: VerifyRawVerity must reject it.
	tampered, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	tampered[123] ^= 0x01
	if err := os.WriteFile(img, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRawVerity(img, info.DataBlocks, info.RootHash); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("tampered VerifyRawVerity = %v, want error with Try:", err)
	}
}

func TestBuildAndVerifyReleaseManifest(t *testing.T) {
	dir := t.TempDir()

	kAMD := bytes.Repeat([]byte("K64"), 500)
	cAMD := []byte("CONFIG_PVH=y\n")
	kARM := bytes.Repeat([]byte("KAR"), 500)
	cARM := []byte("CONFIG_DM_INIT=y\n")
	srcTar := []byte("fake-linux-tarball")
	vmmAMD := []byte("fake-chv-amd64")
	remAMD := []byte("fake-ch-remote-amd64")
	vmmARM := []byte("fake-chv-arm64")
	remARM := []byte("fake-ch-remote-arm64")
	vmmSrc := []byte("fake-chv-src")

	writeAsset := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeAsset("vmlinux-amd64", kAMD)
	writeAsset("config-amd64", cAMD)
	writeAsset("Image-arm64", kARM)
	writeAsset("config-arm64", cARM)
	writeAsset("linux-6.18.62.tar.xz", srcTar)
	writeAsset("cloud-hypervisor-55.0-linux-amd64", vmmAMD)
	writeAsset("ch-remote-55.0-linux-amd64", remAMD)
	writeAsset("cloud-hypervisor-55.0-linux-arm64", vmmARM)
	writeAsset("ch-remote-55.0-linux-arm64", remARM)

	for _, arch := range []string{"amd64", "arm64"} {
		toolsPath := filepath.Join(dir, ToolsAssetName(arch))
		writeAsset(ToolsAssetName(arch), bytes.Repeat([]byte("T"+arch), 1200))
		vi, err := AppendRawVerity(toolsPath)
		if err != nil {
			t.Fatal(err)
		}
		metaBytes, _ := json.Marshal(vi)
		writeAsset(ToolsAssetName(arch)+".json", metaBytes)
	}

	lock := Lock{
		Version: 1,
		Pins: []Pin{
			{Name: "kata-kernel", Repo: "kata-containers/kata-containers", Ref: "4.4.0", Commit: strings.Repeat("a", 40), Watch: "github", Tag: "{version}"},
			{Name: "cloud-hypervisor", Repo: "cloud-hypervisor/cloud-hypervisor", Ref: "v55.0", Commit: strings.Repeat("b", 40), Watch: "github", Tag: "v{version}"},
		},
		Kernel: KernelSpec{
			Source:        "kata-containers",
			KataVersion:   "4.4.0",
			Version:       "6.18.62",
			Licence:       "GPL-2.0-only",
			ArchiveMember: "./vmlinux",
			ConfigMember:  "./config",
			SourceTarball: AssetPin{URL: "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.62.tar.xz", SHA256: testSHA(srcTar), Size: int64(len(srcTar))},
			AMD64: KernelArch{
				URL: "https://example.invalid/kata-amd64.tar.zst", ArchiveSHA256: strings.Repeat("1", 64), ArchiveSize: 10,
				SHA256: testSHA(kAMD), Size: int64(len(kAMD)), ConfigSHA256: testSHA(cAMD), ConfigSize: int64(len(cAMD)),
			},
			ARM64: KernelArch{
				URL: "https://example.invalid/kata-arm64.tar.zst", ArchiveSHA256: strings.Repeat("2", 64), ArchiveSize: 10,
				SHA256: testSHA(kARM), Size: int64(len(kARM)), ConfigSHA256: testSHA(cARM), ConfigSize: int64(len(cARM)),
			},
		},
		VMMs: []VMMSpec{{
			Name: "cloud-hypervisor", Version: "55.0", Licence: "Apache-2.0 AND BSD-3-Clause", HostKernel: ">=6.8",
			Released: "2026-11-19", EOL: "2027-02-11",
			AMD64:       AssetPin{URL: "https://example.invalid/chv-amd64", SHA256: testSHA(vmmAMD), Size: int64(len(vmmAMD))},
			ARM64:       AssetPin{URL: "https://example.invalid/chv-arm64", SHA256: testSHA(vmmARM), Size: int64(len(vmmARM))},
			RemoteAMD64: AssetPin{URL: "https://example.invalid/rem-amd64", SHA256: testSHA(remAMD), Size: int64(len(remAMD))},
			RemoteARM64: AssetPin{URL: "https://example.invalid/rem-arm64", SHA256: testSHA(remARM), Size: int64(len(remARM))},
			Source:      AssetPin{URL: "https://example.invalid/chv-src.tar.xz", SHA256: testSHA(vmmSrc), Size: int64(len(vmmSrc))},
		}},
	}

	m, err := BuildReleaseManifest(BuildManifestOptions{
		Lock:          lock,
		Release:       "0.6.0",
		Runtime:       "2026.11.0",
		BaseURL:       "https://github.com/example/project/releases/download/v0.6.0",
		AssetDir:      dir,
		RequireVMM:    true,
		RequireSource: true,
	})
	if err != nil {
		t.Fatalf("BuildReleaseManifest: %v", err)
	}
	// 2 kernels + 2 tools + 2 cloud-hypervisor + 2 ch-remote = 8 items.
	if len(m.Items) != 8 {
		t.Fatalf("manifest items = %d, want 8: %+v", len(m.Items), m.Items)
	}
	manifestPath := filepath.Join(dir, "release-manifest.json")
	if err := WriteReleaseManifest(manifestPath, m); err != nil {
		t.Fatalf("WriteReleaseManifest: %v", err)
	}
	loaded, err := LoadReleaseManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadReleaseManifest: %v", err)
	}
	if err := VerifyReleaseManifest(loaded, dir); err != nil {
		t.Fatalf("VerifyReleaseManifest: %v", err)
	}

	// Tampering with vmlinux-amd64 fails VerifyReleaseManifest.
	kBad := append([]byte(nil), kAMD...)
	kBad[0] ^= 0xff
	writeAsset("vmlinux-amd64", kBad)
	if err := VerifyReleaseManifest(loaded, dir); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered kernel verify = %v, want SHA-256 failure", err)
	}
}
