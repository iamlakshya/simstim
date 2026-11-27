package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Microsoft/hcsshim/ext4/dmverity"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
)

func writeTestLockAndGuest(t *testing.T) (lockPath, configsDir, guestDir string) {
	t.Helper()
	root := t.TempDir()
	configsDir = filepath.Join(root, "configs")
	guestDir = filepath.Join(root, "guest")
	assetsDir := filepath.Join(guestDir, "assets")
	if err := os.MkdirAll(configsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(assetsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tarBytes := []byte("FAKE-LINUX-TARBALL-BYTES")
	tarSum := sha256.Sum256(tarBytes)
	tarHex := hex.EncodeToString(tarSum[:])

	lockBody := fmt.Sprintf(`version = 1

[[pin]]
name = "kernel"
ref = "v6.18.63"
commit = "1cdda838e8475edf89b73ff0cc952005dd0b1fa7"
watch = "osv"

[[pin]]
name = "erofs-utils"
repo = "erofs/erofs-utils"
ref = "v1.9.4"
commit = "f36cadb5c563995ab3aa8572a60ed6b721b9557d"
watch = "github"

[[pin]]
name = "cloud-hypervisor"
repo = "cloud-hypervisor/cloud-hypervisor"
ref = "v54.1"
commit = "74aeae19cb1ee2c44d33796b0e4e95e1a68e94dd"
watch = "github"

[kernel]
version = "6.18.63"
released = 2026-11-10
url = "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.63.tar.xz"
sha256 = %q

[erofs_utils]
version = "1.9.4"
url = "https://github.com/erofs/erofs-utils/archive/refs/tags/v1.9.4.tar.gz"
sha256 = %q

[moby_check_config]
commit = "70efc0e31da3a115fc87574d1b786fa2b867c346"
sha256 = %q

[builder]
image = "docker.io/library/debian@sha256:%s"
snapshot = "20261110T000000Z"
suite = "trixie"
inrelease_sha256 = "0584fba32e13e0ab8285fb16c27adea1ec03a73669c18702821094fd6ca86675"

[[vmm]]
name = "cloud-hypervisor"
version = "54.1"
released = 2026-11-11
eol = 2027-01-06
amd64 = { url = "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v54.1/cloud-hypervisor-static", sha256 = %q, size = 24 }
arm64 = { url = "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v54.1/cloud-hypervisor-static-aarch64", sha256 = %q, size = 24 }
source = { url = "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v54.1/cloud-hypervisor-v54.1.tar.xz", sha256 = %q, size = 24 }
`, tarHex, tarHex, tarHex, strings.Repeat("9", 64), tarHex, tarHex, tarHex)

	lockPath = filepath.Join(root, "supply.lock")
	if err := os.WriteFile(lockPath, []byte(lockBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "linux-6.18.63.tar.xz"), tarBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, arch := range supply.Arches {
		cfg := []byte("CONFIG_TEST_" + arch + "=y\n")
		if err := os.WriteFile(filepath.Join(configsDir, "config-"+arch), cfg, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(assetsDir, "config-"+arch), cfg, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(assetsDir, supply.KernelAsset(arch)), []byte("KERNEL-"+arch), 0o600); err != nil {
			t.Fatal(err)
		}
		toolsData := bytes.Repeat([]byte("T"), 8192)
		tree, err := dmverity.MerkleTree(bytes.NewReader(toolsData))
		if err != nil {
			t.Fatal(err)
		}
		toolsBytes := slices.Concat(toolsData, tree)
		toolsSum := sha256.Sum256(toolsBytes)
		if err := os.WriteFile(filepath.Join(assetsDir, supply.ToolsAsset(arch)), toolsBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		vm := verityMeta{
			SHA256:     hex.EncodeToString(toolsSum[:]),
			Size:       int64(len(toolsBytes)),
			RootHash:   hex.EncodeToString(dmverity.RootHash(tree)),
			Salt:       strings.Repeat("0", 64),
			DataBlocks: 2,
		}
		vmBytes, err := json.Marshal(vm)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(guestDir, "tools-"+arch+".verity.json"), vmBytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return lockPath, configsDir, guestDir
}

func TestWriteAssetsAndCheckWithBootPair(t *testing.T) {
	useList(t, resolvedAt("0.2.0"))
	dir := helpersDir(t)
	lockPath, configsDir, guestDir := writeTestLockAndGuest(t)

	var out bytes.Buffer
	if err := run([]string{"write", "-release", "v0.6.0", "-guest", guestDir, "-lock", lockPath, "-configs", configsDir, "-require-guest", dir}, &out); err != nil {
		t.Fatalf("write with boot pair: %v", err)
	}
	manifest := filepath.Join(dir, supply.FileName)
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	m, err := supply.Load(b)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Items) != 8 {
		t.Fatalf("items = %d, want 8", len(m.Items))
	}

	k, ok := m.Find(supply.KindKernel, "guest", "amd64")
	if !ok || k.Version != "6.18.63" || k.Released != "2026-11-10" || k.Licence != "GPL-2.0-only" ||
		k.URL != release+"/vmlinux-amd64" || k.SourceURL != release+"/linux-6.18.63.tar.xz" {
		t.Fatalf("amd64 kernel = %+v, %v", k, ok)
	}
	tl, ok := m.Find(supply.KindTools, "tools", "arm64")
	if !ok || !supply.IsHex64(tl.RootHash) || tl.Salt != strings.Repeat("0", 64) || tl.DataBlocks != 2 || tl.URL != release+"/tools-arm64.erofs" {
		t.Fatalf("arm64 tools = %+v, %v", tl, ok)
	}

	out.Reset()
	if err := run([]string{"assets", manifest}, &out); err != nil {
		t.Fatal(err)
	}
	gotAssets := strings.Fields(out.String())
	wantAssets := []string{
		brand.Name + "-host-linux-amd64",
		brand.Name + "-host-linux-arm64",
		brand.Name + "-vt-linux-amd64",
		brand.Name + "-vt-linux-arm64",
		"vmlinux-amd64",
		"tools-amd64.erofs",
		"Image-arm64",
		"tools-arm64.erofs",
		"linux-6.18.63.tar.xz",
		"config-amd64",
		"config-arm64",
	}
	if !slices.Equal(gotAssets, wantAssets) {
		t.Fatalf("assets = %v, want %v", gotAssets, wantAssets)
	}

	// Copy guest assets into the download folder and run check -require-guest.
	dl := upload(t, dir)
	entries, err := os.ReadDir(filepath.Join(guestDir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(guestDir, "assets", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dl, e.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out.Reset()
	if err := run([]string{"check", "-require-guest", "-lock", lockPath, "-configs", configsDir, "-url", release, "-dir", dl, manifest}, &out); err != nil {
		t.Fatalf("check with boot pair: %v", err)
	}

	// Corrupting linux-6.18.63.tar.xz produces spec 20.1's exact error and Try hint.
	if err := os.WriteFile(filepath.Join(dl, "linux-6.18.63.tar.xz"), []byte("TAMPERED"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"check", "-require-guest", "-lock", lockPath, "-configs", configsDir, "-url", release, "-dir", dl, manifest}, &bytes.Buffer{})
	var f *failure
	if !errors.As(err, &f) || f.msg != "linux-6.18.63.tar.xz does not match its SHA-256 in guest/supply.lock." ||
		!strings.Contains(f.try, "sha256sums.asc") {
		t.Fatalf("tampered kernel tarball: %#v", err)
	}
}

func TestCheckRequireGuestRefusesMissingBootPair(t *testing.T) {
	useList(t, resolvedAt("0.2.0"))
	dir := helpersDir(t)
	if err := run([]string{"write", "-release", "0.6.0", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, supply.FileName)
	err := run([]string{"check", "-require-guest", "-url", release, "-dir", upload(t, dir), manifest}, &bytes.Buffer{})
	var f *failure
	if !errors.As(err, &f) || !strings.Contains(f.msg, "lacks the amd64 guest kernel") || f.try == "" {
		t.Fatalf("expected missing kernel error, got %#v", err)
	}
}

func TestPinGetAndVerify(t *testing.T) {
	lockPath, _, guestDir := writeTestLockAndGuest(t)
	for key, want := range map[string]string{
		"kernel.version":                  "6.18.63",
		"kernel.released":                 "2026-11-10",
		"erofs.version":                   "1.9.4",
		"moby.commit":                     "70efc0e31da3a115fc87574d1b786fa2b867c346",
		"builder.snapshot":                "20261110T000000Z",
		"builder.suite":                   "trixie",
		"builder.inrelease_sha256":        "0584fba32e13e0ab8285fb16c27adea1ec03a73669c18702821094fd6ca86675",
		"vmm.cloud-hypervisor.version":    "54.1",
		"vmm.cloud-hypervisor.amd64.size": "24",
		"vmm.cloud-hypervisor.arm64.size": "24",
	} {
		var out bytes.Buffer
		if err := run([]string{"pin", "-lock", lockPath, "get", key}, &out); err != nil {
			t.Fatalf("pin get %s: %v", key, err)
		}
		if got := strings.TrimSpace(out.String()); got != want {
			t.Errorf("pin get %s = %q, want %q", key, got, want)
		}
	}

	tarFile := filepath.Join(guestDir, "assets", "linux-6.18.63.tar.xz")
	for _, target := range []string{"kernel", "erofs", "moby", "vmm:cloud-hypervisor:amd64", "vmm:cloud-hypervisor:arm64", "vmm:cloud-hypervisor:source"} {
		if err := run([]string{"pin", "-lock", lockPath, "verify", target, tarFile}, &bytes.Buffer{}); err != nil {
			t.Errorf("pin verify %s: %v", target, err)
		}
	}

	bad := filepath.Join(t.TempDir(), "linux-6.18.63.tar.xz")
	if err := os.WriteFile(bad, []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"pin", "-lock", lockPath, "verify", "kernel", bad}, &bytes.Buffer{})
	var f *failure
	if !errors.As(err, &f) || f.msg != "linux-6.18.63.tar.xz does not match its SHA-256 in guest/supply.lock." {
		t.Errorf("pin verify bad kernel: %#v", err)
	}
}
