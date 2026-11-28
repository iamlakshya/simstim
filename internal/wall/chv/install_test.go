package chv

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
)

func buildTestZstdTar(t *testing.T, member string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     member,
		Mode:     0o644,
		Size:     int64(len(payload)),
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

func sha256HexBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestExtractPinnedKernelFromZstdTar(t *testing.T) {
	dir := t.TempDir()
	member := "./opt/kata/share/kata-containers/vmlinux-6.18.62-211"
	kernelBytes := bytes.Repeat([]byte("KATA-VMLINUX-TEST-"), 256)
	archiveBytes := buildTestZstdTar(t, member, kernelBytes)
	archivePath := filepath.Join(dir, "kata.tar.zst")
	if err := os.WriteFile(archivePath, archiveBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	pin := KernelPin{
		ArchiveMember: member,
		KernelSHA256:  sha256HexBytes(kernelBytes),
		KernelSize:    int64(len(kernelBytes)),
	}
	dst := filepath.Join(dir, "vmlinux")
	if err := extractPinnedKernelFromZstdTar(context.Background(), archivePath, dst, pin); err != nil {
		t.Fatalf("extractPinnedKernelFromZstdTar: %v", err)
	}
	if err := VerifyFileSHA256(dst, pin.KernelSHA256); err != nil {
		t.Fatalf("VerifyFileSHA256: %v", err)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("vmlinux mode = %v, %v; want 0444", fi, err)
	}

	// Wrong SHA-256 fails closed without installing dst.
	badPin := pin
	badPin.KernelSHA256 = strings.Repeat("0", 64)
	badDst := filepath.Join(dir, "vmlinux-bad")
	if err := extractPinnedKernelFromZstdTar(context.Background(), archivePath, badDst, badPin); err == nil || !strings.Contains(err.Error(), "SHA-256") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("bad SHA-256 err = %v, want SHA-256 error with Try hint", err)
	}
	if _, err := os.Stat(badDst); !os.IsNotExist(err) {
		t.Fatalf("corrupted kernel must not be installed at %s", badDst)
	}

	// Missing member fails cleanly with Try hint.
	missingPin := pin
	missingPin.ArchiveMember = "./opt/kata/share/kata-containers/missing"
	if err := extractPinnedKernelFromZstdTar(context.Background(), archivePath, filepath.Join(dir, "vmlinux-miss"), missingPin); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("missing member err = %v, want error with Try hint", err)
	}
}

func TestBuildAndEnsureToolsImageExt4FallbackAndCmdlineDetect(t *testing.T) {
	// Hide mkfs.erofs so BuildToolsImage exercises the pure-Go tar2ext4 fallback.
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	helper := filepath.Join(dir, "fake-host-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho wall-init-v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "tools.erofs")

	// Without AllowExt4Fallback, missing mkfs.erofs still fails with Try hint.
	if _, err := BuildToolsImage(context.Background(), helper, out, ToolsBuildOptions{}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("BuildToolsImage without fallback = %v, want error with Try hint", err)
	}

	// With AllowExt4Fallback, EnsureToolsImage builds verified ext4 + raw dm-verity.
	rec1, err := EnsureToolsImage(context.Background(), helper, out, ToolsBuildOptions{AllowExt4Fallback: true})
	if err != nil {
		t.Fatalf("EnsureToolsImage (ext4 fallback): %v", err)
	}
	if rec1.FSType != "ext4" || rec1.DataBlocks == 0 || len(rec1.RootHash) != 64 || len(rec1.HelperSHA256) != 64 {
		t.Fatalf("unexpected ext4 ToolsRecord: %+v", rec1)
	}
	if _, err := supply.VerifyRawVerity(out, rec1.DataBlocks, rec1.RootHash); err != nil {
		t.Fatalf("VerifyRawVerity(ext4 tools): %v", err)
	}

	// BuildKernelCmdline auto-detects ext4 magic even when DiskVerity.FSType is empty.
	cmdline, err := BuildKernelCmdline(VMConfig{
		Tools: DiskVerity{
			Path:       out,
			RootHash:   rec1.RootHash,
			DataBlocks: rec1.DataBlocks,
		},
	})
	if err != nil {
		t.Fatalf("BuildKernelCmdline: %v", err)
	}
	if !strings.Contains(cmdline, "rootfstype=ext4") {
		t.Fatalf("cmdline missing rootfstype=ext4 for ext4 tools image:\n%s", cmdline)
	}

	// Second EnsureToolsImage reuses unchanged verified image.
	fi1, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	rec2, err := EnsureToolsImage(context.Background(), helper, out, ToolsBuildOptions{AllowExt4Fallback: true})
	if err != nil {
		t.Fatalf("EnsureToolsImage reuse: %v", err)
	}
	if rec2.SHA256 != rec1.SHA256 || rec2.RootHash != rec1.RootHash {
		t.Fatalf("EnsureToolsImage reuse mismatch: %+v vs %+v", rec2, rec1)
	}
	fi2, err := os.Stat(out)
	if err != nil || !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Fatalf("expected EnsureToolsImage to reuse existing tools image without rewrite")
	}

	// Changing helper binary triggers automatic rebuild with updated HelperSHA256.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho wall-init-v2-upgraded\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec3, err := EnsureToolsImage(context.Background(), helper, out, ToolsBuildOptions{AllowExt4Fallback: true})
	if err != nil {
		t.Fatalf("EnsureToolsImage after helper upgrade: %v", err)
	}
	if rec3.HelperSHA256 == rec1.HelperSHA256 || rec3.RootHash == rec1.RootHash {
		t.Fatalf("expected rebuilt tools image after helper upgrade: old=%+v new=%+v", rec1, rec3)
	}
}

func TestInstallRuntimeAndProbeFixCommand(t *testing.T) {
	// Hide mkfs.erofs so test runs deterministically everywhere without erofs-utils.
	t.Setenv("PATH", t.TempDir())

	vmmBytes := []byte("fake-cloud-hypervisor-static-v55")
	remoteBytes := []byte("fake-ch-remote-static-v55")
	kernelBytes := bytes.Repeat([]byte("FAKE-KATA-KERNEL-61862-"), 128)
	const member = "./opt/kata/share/kata-containers/vmlinux-6.18.62-211"
	archiveBytes := buildTestZstdTar(t, member, kernelBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vmm":
			_, _ = w.Write(vmmBytes)
		case "/ch-remote":
			_, _ = w.Write(remoteBytes)
		case "/kata.tar.zst":
			_, _ = w.Write(archiveBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origPin := Pins["amd64"]
	t.Cleanup(func() { Pins["amd64"] = origPin })
	Pins["amd64"] = ArchPins{
		Arch: "amd64",
		VMM: FilePin{
			URL:    srv.URL + "/vmm",
			SHA256: sha256HexBytes(vmmBytes),
			Size:   int64(len(vmmBytes)),
		},
		CHRemote: FilePin{
			URL:    srv.URL + "/ch-remote",
			SHA256: sha256HexBytes(remoteBytes),
			Size:   int64(len(remoteBytes)),
		},
		Kernel: KernelPin{
			Archive: FilePin{
				URL:    srv.URL + "/kata.tar.zst",
				SHA256: sha256HexBytes(archiveBytes),
				Size:   int64(len(archiveBytes)),
			},
			ArchiveMember: member,
			KernelSHA256:  sha256HexBytes(kernelBytes),
			KernelSize:    int64(len(kernelBytes)),
		},
	}

	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime", RuntimeLabel)
	helper := filepath.Join(root, "fake-host-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho helper\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := InstallRuntime(context.Background(), InstallOptions{
		Dir:        runtimeDir,
		Arch:       "amd64",
		HelperBin:  helper,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("InstallRuntime: %v", err)
	}
	if res.Tools == nil || res.Tools.FSType != "ext4" || res.Dir != runtimeDir {
		t.Fatalf("unexpected InstallResult: %+v", res)
	}

	// Verify receipt file exists beside runtimeDir with expected runtime label.
	receiptPath := filepath.Join(root, "runtime", RuntimeLabel+".json")
	rawReceipt, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("read runtime receipt: %v", err)
	}
	var receipt struct {
		Kind    string `json:"kind"`
		Runtime string `json:"runtime"`
		Arch    string `json:"arch"`
	}
	if err := json.Unmarshal(rawReceipt, &receipt); err != nil || receipt.Kind != DriverName || receipt.Runtime != RuntimeLabel || receipt.Arch != "amd64" {
		t.Fatalf("unexpected receipt %q: %v", string(rawReceipt), err)
	}

	// ProbeWith succeeds once all 4 artefacts + metadata are installed.
	mkfsPath := filepath.Join(root, "mkfs.ext4")
	if err := os.WriteFile(mkfsPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	probeWithDir := func() ProbeDeps {
		return ProbeDeps{
			GOOS:          "linux",
			GOARCH:        "amd64",
			KernelRelease: func() string { return "6.12.118+deb13-cloud-amd64" },
			Stat: func(p string) (os.FileInfo, error) {
				if p == devKVMPath {
					return fakeFileInfo{name: "kvm"}, nil
				}
				return os.Stat(p)
			},
			CheckKVMRW: func(string) error { return nil },
			LookPath: func(name string) (string, error) {
				if name == "mkfs.ext4" {
					return mkfsPath, nil
				}
				return "", os.ErrNotExist
			},
			RuntimePaths: func() RuntimePaths { return RuntimePathsIn(runtimeDir) },
			CurrentUser:  func() string { return "alice" },
			RunCmd: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte("cloud-hypervisor v55.0\n"), nil
			},
		}
	}

	p := ProbeWith(context.Background(), probeWithDir())
	if !p.OK || p.Version != "55.0" || p.Facts["tools"] != res.Paths.Tools {
		t.Fatalf("probe after InstallRuntime = %+v, want OK", p)
	}

	// Removing tools.erofs.json makes probe fail with exact `wall install microvm` fix command.
	if err := os.Remove(res.Paths.Tools + ".json"); err != nil {
		t.Fatal(err)
	}
	pMiss := ProbeWith(context.Background(), probeWithDir())
	wantFix := brand.HostBinary + " wall install microvm"
	if pMiss.OK || pMiss.Fix != wantFix {
		t.Fatalf("probe missing tools meta = %+v, want Fix=%q", pMiss, wantFix)
	}
}
