package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestCheckLockCommand(t *testing.T) {
	lockPath := filepath.Join("..", "..", "guest", "supply.lock")
	if err := run(context.Background(), []string{"check-lock", "--lock", lockPath}); err != nil {
		t.Fatalf("check-lock on repo lock failed: %v", err)
	}
}

func TestFetchKernelMirrorVMMBuildToolsAndManifest(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not installed")
	}
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	if _, err := exec.LookPath("mkfs.erofs"); err != nil {
		t.Skip("mkfs.erofs not installed")
	}

	tmp := t.TempDir()
	// Create a valid Kata-like tar.zst archive containing kernel + passing config.
	stage := filepath.Join(tmp, "stage")
	kataDir := filepath.Join(stage, "opt", "kata", "share", "kata-containers")
	if err := os.MkdirAll(kataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kernBytes := bytes.Repeat([]byte("VMLINUX"), 1024)
	cfgBytes := []byte(strings.Join([]string{
		"CONFIG_PVH=y",
		"CONFIG_ACPI=y",
		"CONFIG_ACPI_BUTTON=y",
		"CONFIG_VIRTIO_PCI=y",
		"CONFIG_VIRTIO_BLK=y",
		"CONFIG_VIRTIO_CONSOLE=y",
		"CONFIG_VIRTIO_BALLOON=y",
		"CONFIG_PAGE_REPORTING=y",
		"CONFIG_VSOCKETS=y",
		"CONFIG_VIRTIO_VSOCKETS=y",
		"CONFIG_BLK_DEV_DM=y",
		"CONFIG_DM_INIT=y",
		"CONFIG_DM_VERITY=y",
		"CONFIG_EROFS_FS=y",
		"CONFIG_EXT4_FS=y",
		"CONFIG_OVERLAY_FS=y",
		"CONFIG_TMPFS=y",
		"CONFIG_DEVTMPFS=y",
		"CONFIG_NF_TABLES=y",
		"CONFIG_NFT_REDIR=y",
		"CONFIG_CGROUPS=y",
		"CONFIG_MEMCG=y",
		"CONFIG_SECURITY_LANDLOCK=y",
		`CONFIG_LSM="landlock"`,
		"CONFIG_SECCOMP_FILTER=y",
		"CONFIG_USER_NS=y",
		"CONFIG_FUSE_FS=y",
		"CONFIG_RANDSTRUCT_NONE=y",
		"CONFIG_DEBUG_INFO_NONE=y",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(kataDir, "vmlinux-6.18.62-211"), kernBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kataDir, "config-6.18.62-211"), cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(tmp, "kata.tar.zst")
	if out, err := exec.CommandContext(context.Background(), "tar", "--zstd", "-cf", archivePath, "-C", stage,
		"./opt/kata/share/kata-containers/vmlinux-6.18.62-211",
		"./opt/kata/share/kata-containers/config-6.18.62-211").CombinedOutput(); err != nil {
		t.Fatalf("create test tar.zst: %v\n%s", err, out)
	}
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	vmmBytes := []byte("#!/bin/sh\necho chv\n")
	remBytes := []byte("#!/bin/sh\necho ch-remote\n")
	srcBytes := []byte("kernel-source-tarball")
	vmmSrcBytes := []byte("vmm-source-tarball")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kata.tar.zst":
			_, _ = w.Write(archiveBytes)
		case "/linux-6.18.62.tar.xz":
			_, _ = w.Write(srcBytes)
		case "/chv":
			_, _ = w.Write(vmmBytes)
		case "/ch-remote":
			_, _ = w.Write(remBytes)
		case "/chv-src.tar.xz":
			_, _ = w.Write(vmmSrcBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldHTTP := supplyHTTPClient
	supplyHTTPClient = srv.Client
	t.Cleanup(func() { supplyHTTPClient = oldHTTP })

	lockPath := filepath.Join(tmp, "supply.lock")
	lockText := fmt.Sprintf(`version = 1

[[pin]]
name = "cloud-hypervisor"
repo = "cloud-hypervisor/cloud-hypervisor"
ref = "v55.0"
commit = "%s"
watch = "github"
tag = "v{version}"

[[pin]]
name = "kata-kernel"
repo = "kata-containers/kata-containers"
ref = "4.4.0"
commit = "%s"
watch = "github"
tag = "{version}"

[kernel]
source = "kata-containers"
kata_version = "4.4.0"
version = "6.18.62"
licence = "GPL-2.0-only"
archive_member = "./opt/kata/share/kata-containers/vmlinux-6.18.62-211"
config_member = "./opt/kata/share/kata-containers/config-6.18.62-211"
source_tarball = { url = "%s/linux-6.18.62.tar.xz", sha256 = "%s", size = %d }
amd64 = { url = "%s/kata.tar.zst", archive_sha256 = "%s", archive_size = %d, sha256 = "%s", size = %d, config_sha256 = "%s", config_size = %d }
arm64 = { url = "%s/kata.tar.zst", archive_sha256 = "%s", archive_size = %d, sha256 = "%s", size = %d, config_sha256 = "%s", config_size = %d }

[[vmm]]
name = "cloud-hypervisor"
version = "55.0"
licence = "Apache-2.0 AND BSD-3-Clause"
host_kernel = ">=6.8"
released = 2026-11-19
eol = 2027-02-11
amd64 = { url = "%s/chv", sha256 = "%s", size = %d }
arm64 = { url = "%s/chv", sha256 = "%s", size = %d }
remote_amd64 = { url = "%s/ch-remote", sha256 = "%s", size = %d }
remote_arm64 = { url = "%s/ch-remote", sha256 = "%s", size = %d }
source = { url = "%s/chv-src.tar.xz", sha256 = "%s", size = %d }
`,
		strings.Repeat("a", 40), strings.Repeat("b", 40),
		srv.URL, shaHex(srcBytes), len(srcBytes),
		srv.URL, shaHex(archiveBytes), len(archiveBytes), shaHex(kernBytes), len(kernBytes), shaHex(cfgBytes), len(cfgBytes),
		srv.URL, shaHex(archiveBytes), len(archiveBytes), shaHex(kernBytes), len(kernBytes), shaHex(cfgBytes), len(cfgBytes),
		srv.URL, shaHex(vmmBytes), len(vmmBytes),
		srv.URL, shaHex(vmmBytes), len(vmmBytes),
		srv.URL, shaHex(remBytes), len(remBytes),
		srv.URL, shaHex(remBytes), len(remBytes),
		srv.URL, shaHex(vmmSrcBytes), len(vmmSrcBytes),
	)
	if err := os.WriteFile(lockPath, []byte(lockText), 0o600); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(tmp, "out")
	checkScript := filepath.Join("..", "..", "guest", "kernel", "check-config.sh")
	ctx := context.Background()

	if err := run(ctx, []string{"fetch-kernel", "--lock", lockPath, "--arch", "amd64", "--out", outDir, "--check-config", checkScript, "--mirror-source"}); err != nil {
		t.Fatalf("fetch-kernel: %v", err)
	}
	if err := run(ctx, []string{"mirror-vmm", "--lock", lockPath, "--arch", "amd64", "--out", outDir, "--source"}); err != nil {
		t.Fatalf("mirror-vmm: %v", err)
	}

	fakeHelper := filepath.Join(tmp, "helper")
	if err := os.WriteFile(fakeHelper, []byte("#!/bin/sh\necho init\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"build-tools", "--arch", "amd64", "--helper", fakeHelper, "--out", outDir, "--epoch", "1700000000"}); err != nil {
		t.Fatalf("build-tools: %v", err)
	}

	manifestPath := filepath.Join(outDir, "release-manifest.json")
	if err := run(ctx, []string{
		"manifest",
		"--lock", lockPath,
		"--release", "0.6.0",
		"--runtime", "2026.11.0",
		"--base-url", "https://github.com/example/project/releases/download/v0.6.0",
		"--dir", outDir,
		"--out", manifestPath,
		"--arch", "amd64",
		"--require-vmm",
		"--require-source",
	}); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := run(ctx, []string{"verify-manifest", "--manifest", manifestPath, "--dir", outDir}); err != nil {
		t.Fatalf("verify-manifest: %v", err)
	}
}

func TestBootTestRelativeDirPinsAndControl(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("boot-test executes a POSIX shell fake VMM on Linux")
	}
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		t.Skipf("unsupported test arch %s", arch)
	}

	writeFixture := func(t *testing.T, vmmBody string) (relDir, lockPath, kernelPath, vmmPath string) {
		t.Helper()
		tmp := t.TempDir()
		t.Chdir(tmp)

		relDir = filepath.Join("build", "guest")
		if err := os.MkdirAll(relDir, 0o700); err != nil {
			t.Fatal(err)
		}
		kernelName, err := supply.KernelAssetName(arch)
		if err != nil {
			t.Fatal(err)
		}
		kernelBytes := bytes.Repeat([]byte("KERNEL"), 128)
		kernelPath = filepath.Join(relDir, kernelName)
		if err := os.WriteFile(kernelPath, kernelBytes, 0o600); err != nil {
			t.Fatal(err)
		}

		toolsPath := filepath.Join(relDir, supply.ToolsAssetName(arch))
		if err := os.WriteFile(toolsPath, bytes.Repeat([]byte("T"), 4096), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := supply.AppendRawVerity(toolsPath)
		if err != nil {
			t.Fatal(err)
		}
		metaJSON := fmt.Sprintf(`{"sha256":%q,"size":%d,"root_hash":%q,"salt":%q,"data_blocks":%d}`,
			info.SHA256, info.Size, info.RootHash, info.Salt, info.DataBlocks)
		if err := os.WriteFile(toolsPath+".json", []byte(metaJSON), 0o600); err != nil {
			t.Fatal(err)
		}

		vmmBytes := []byte(vmmBody)
		vmmPath = filepath.Join(relDir, supply.VMMAssetName("cloud-hypervisor", "55.0", arch))
		if err := os.WriteFile(vmmPath, vmmBytes, 0o700); err != nil {
			t.Fatal(err)
		}
		remBytes := []byte("#!/bin/sh\nexit 0\n")
		remPath := filepath.Join(relDir, supply.VMMRemoteAssetName("55.0", arch))
		if err := os.WriteFile(remPath, remBytes, 0o700); err != nil {
			t.Fatal(err)
		}

		// Place another VMM BEFORE cloud-hypervisor to prove FindVMM("cloud-hypervisor") is used.
		otherVMM := []byte("#!/bin/sh\necho WRONG-VMM >&2\nexit 99\n")
		dummyHash := shaHex([]byte("dummy"))
		lockText := fmt.Sprintf(`version = 1

[[pin]]
name = "other-vmm"
repo = "example/other-vmm"
ref = "v1.0"
commit = "%s"
watch = "github"
tag = "v{version}"

[[pin]]
name = "cloud-hypervisor"
repo = "cloud-hypervisor/cloud-hypervisor"
ref = "v55.0"
commit = "%s"
watch = "github"
tag = "v{version}"

[[pin]]
name = "kata-kernel"
repo = "kata-containers/kata-containers"
ref = "4.4.0"
commit = "%s"
watch = "github"
tag = "{version}"

[kernel]
source = "kata-containers"
kata_version = "4.4.0"
version = "6.18.62"
licence = "GPL-2.0-only"
archive_member = "./vmlinux"
config_member = "./config"
source_tarball = { url = "https://example.invalid/linux.tar.xz", sha256 = "%s", size = 5 }
amd64 = { url = "https://example.invalid/kata.tar.zst", archive_sha256 = "%s", archive_size = 5, sha256 = "%s", size = %d, config_sha256 = "%s", config_size = 5 }
arm64 = { url = "https://example.invalid/kata.tar.zst", archive_sha256 = "%s", archive_size = 5, sha256 = "%s", size = %d, config_sha256 = "%s", config_size = 5 }

[[vmm]]
name = "other-vmm"
version = "1.0"
licence = "Apache-2.0"
amd64 = { url = "https://example.invalid/other", sha256 = "%s", size = %d }
arm64 = { url = "https://example.invalid/other", sha256 = "%s", size = %d }
remote_amd64 = { url = "https://example.invalid/other-rem", sha256 = "%s", size = %d }
remote_arm64 = { url = "https://example.invalid/other-rem", sha256 = "%s", size = %d }
source = { url = "https://example.invalid/other-src", sha256 = "%s", size = 5 }

[[vmm]]
name = "cloud-hypervisor"
version = "55.0"
licence = "Apache-2.0 AND BSD-3-Clause"
host_kernel = ">=6.8"
amd64 = { url = "https://example.invalid/chv", sha256 = "%s", size = %d }
arm64 = { url = "https://example.invalid/chv", sha256 = "%s", size = %d }
remote_amd64 = { url = "https://example.invalid/ch-remote", sha256 = "%s", size = %d }
remote_arm64 = { url = "https://example.invalid/ch-remote", sha256 = "%s", size = %d }
source = { url = "https://example.invalid/chv-src", sha256 = "%s", size = 5 }
`,
			strings.Repeat("c", 40), strings.Repeat("a", 40), strings.Repeat("b", 40),
			dummyHash,
			dummyHash, shaHex(kernelBytes), len(kernelBytes), dummyHash,
			dummyHash, shaHex(kernelBytes), len(kernelBytes), dummyHash,
			shaHex(otherVMM), len(otherVMM), shaHex(otherVMM), len(otherVMM),
			shaHex(remBytes), len(remBytes), shaHex(remBytes), len(remBytes), dummyHash,
			shaHex(vmmBytes), len(vmmBytes), shaHex(vmmBytes), len(vmmBytes),
			shaHex(remBytes), len(remBytes), shaHex(remBytes), len(remBytes), dummyHash,
		)
		lockPath = filepath.Join("guest", "supply.lock")
		if err := os.MkdirAll("guest", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockPath, []byte(lockText), 0o600); err != nil {
			t.Fatal(err)
		}
		return relDir, lockPath, kernelPath, vmmPath
	}

	t.Run("passes with relative dir and selects cloud-hypervisor by name", func(t *testing.T) {
		relDir, lockPath, _, _ := writeFixture(t, "#!/bin/sh\necho \"boot args: $*\" >&2\necho \"self-test ok\" >&2\n")
		err := run(context.Background(), []string{"boot-test", "--arch", arch, "--dir", relDir, "--lock", lockPath, "--timeout", "5s"})
		if err != nil {
			t.Fatalf("boot-test with relative dir failed: %v", err)
		}
	})

	t.Run("fails when self-test ok is missing", func(t *testing.T) {
		relDir, lockPath, _, _ := writeFixture(t, "#!/bin/sh\necho \"kernel panic\" >&2\n")
		err := run(context.Background(), []string{"boot-test", "--arch", arch, "--dir", relDir, "--lock", lockPath, "--timeout", "5s"})
		if err == nil || !strings.Contains(err.Error(), "self-test ok") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("expected missing self-test ok error with Try hint, got: %v", err)
		}
	})

	t.Run("rejects tampered kernel and VMM before executing VMM", func(t *testing.T) {
		relDir, lockPath, kernelPath, vmmPath := writeFixture(t, "#!/bin/sh\necho \"self-test ok\" >&2\n")
		if err := os.WriteFile(kernelPath, []byte("TAMPERED"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := run(context.Background(), []string{"boot-test", "--arch", arch, "--dir", relDir, "--lock", lockPath, "--timeout", "5s"})
		if err == nil || !strings.Contains(err.Error(), "pin verification") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("expected kernel pin verification failure, got: %v", err)
		}

		relDir2, lockPath2, _, vmmPath2 := writeFixture(t, "#!/bin/sh\necho \"self-test ok\" >&2\n")
		if err := os.WriteFile(vmmPath2, []byte("#!/bin/sh\necho \"self-test ok\" >&2\n# tampered\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = vmmPath
		err = run(context.Background(), []string{"boot-test", "--arch", arch, "--dir", relDir2, "--lock", lockPath2, "--timeout", "5s"})
		if err == nil || !strings.Contains(err.Error(), "pin verification") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("expected VMM pin verification failure, got: %v", err)
		}
	})

	t.Run("enforces timeout with valid just guest-boot hint", func(t *testing.T) {
		relDir, lockPath, _, _ := writeFixture(t, "#!/bin/sh\nexec sleep 30\n")
		err := run(context.Background(), []string{"boot-test", "--arch", arch, "--dir", relDir, "--lock", lockPath, "--timeout", "150ms"})
		if err == nil || !strings.Contains(err.Error(), "just guest-boot "+arch) || strings.Contains(err.Error(), "ARCH=") {
			t.Fatalf("expected timeout error with positional `just guest-boot %s` hint, got: %v", arch, err)
		}
	})

	t.Run("honours context cancellation immediately", func(t *testing.T) {
		relDir, lockPath, _, _ := writeFixture(t, "#!/bin/sh\nexec sleep 30\n")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := run(ctx, []string{"boot-test", "--arch", arch, "--dir", relDir, "--lock", lockPath, "--timeout", "30s"})
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("expected cancellation error, got: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("boot-test ignored ctx.Done() and took %s", elapsed)
		}
	})
}
