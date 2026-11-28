// Package chv implements a thin Cloud Hypervisor microvm wall driver
// (isolation spec 20.3, 20.10; decision 0053).
package chv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

// DriverName is the registered wall driver name for Cloud Hypervisor.
const DriverName = "chv"

// Pinned release versions matching guest/supply.lock.
const (
	PinnedVMMVersion    = "55.0"
	AdvisoryFloor       = "52.0"
	PinnedKataVersion   = "4.4.0"
	PinnedKernelVersion = "6.18.62"
	RuntimeLabel        = "chv-v" + PinnedVMMVersion + "-k" + PinnedKernelVersion
)

// FilePin describes one pinned downloadable artefact.
type FilePin struct {
	URL    string
	SHA256 string
	Size   int64
}

// KernelPin describes the pinned prebuilt Kata 6.18 guest kernel archive and
// extracted vmlinux image and config for one architecture.
type KernelPin struct {
	Archive       FilePin
	ArchiveMember string
	ConfigMember  string
	KernelSHA256  string
	KernelSize    int64
	ConfigSHA256  string
	ConfigSize    int64
}

// ArchPins holds all pinned microvm runtime artefacts for one architecture.
type ArchPins struct {
	Arch     string
	VMM      FilePin
	CHRemote FilePin
	Kernel   KernelPin
}

// Pins holds the per-architecture pins matching guest/supply.lock.
var Pins = map[string]ArchPins{
	"amd64": {
		Arch: "amd64",
		VMM: FilePin{
			URL:    "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v55.0/cloud-hypervisor-static",
			SHA256: "ce7133bf535ed5a4e2a63ea1f26025445a5b1241f73ddfb81ff2fbcb2e803a59",
			Size:   7709448,
		},
		CHRemote: FilePin{
			URL:    "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v55.0/ch-remote-static",
			SHA256: "2a77651c6a628a5a0edf9ec07b79a2996104577662489ec670ae65511f17ba69",
			Size:   1135192,
		},
		Kernel: KernelPin{
			Archive: FilePin{
				URL:    "https://github.com/kata-containers/kata-containers/releases/download/4.4.0/kata-static-substrate-4.4.0-amd64.tar.zst",
				SHA256: "9f2596cd3aecf524be06036b6edf4991a8978b41a071f8676b3efd2c23d078a7",
				Size:   41962312,
			},
			ArchiveMember: "./opt/kata/share/kata-containers/vmlinux-6.18.62-211",
			ConfigMember:  "./opt/kata/share/kata-containers/config-6.18.62-211",
			KernelSHA256:  "011100a36d401b7eff9c4ab132bd570c9a4f9bd7a70550d32b0aaa6cb1455027",
			KernelSize:    40868128,
			ConfigSHA256:  "0127c096083a883f9701a44249cb343f1c33f137d05f6e6184d1b3b833f26de6",
			ConfigSize:    97258,
		},
	},
	"arm64": {
		Arch: "arm64",
		VMM: FilePin{
			URL:    "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v55.0/cloud-hypervisor-static-aarch64",
			SHA256: "446de7552dacc83bc86d1137e7ffa7c2d204cdda4850eac4f2d2666d63c0f697",
			Size:   6054008,
		},
		CHRemote: FilePin{
			URL:    "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v55.0/ch-remote-static-aarch64",
			SHA256: "b637ca7ecba7e1cbe020926a1137504ad59ff7936f005fe593a9d0353e8583fb",
			Size:   970744,
		},
		Kernel: KernelPin{
			Archive: FilePin{
				URL:    "https://github.com/kata-containers/kata-containers/releases/download/4.4.0/kata-static-substrate-4.4.0-arm64.tar.zst",
				SHA256: "227cb3de0e98da47d5d9b3c48f263aa81e232345e6b1ce56de4dc374cf178e41",
				Size:   38348343,
			},
			ArchiveMember: "./opt/kata/share/kata-containers/vmlinux-6.18.62-211",
			ConfigMember:  "./opt/kata/share/kata-containers/config-6.18.62-211",
			KernelSHA256:  "daa09cde28472ee6afad203a20c94a6ce1c96c9d8dffafdd4c0e87c59262d64a",
			KernelSize:    18883072,
			ConfigSHA256:  "5a38f40e5bd7003ebfdab61cbf473974364ca6e2bc07611b40b84aa07eb38efe",
			ConfigSize:    95915,
		},
	},
}

// PinsForArch returns the pinned artefacts for arch ("amd64" or "arm64").
func PinsForArch(arch string) (ArchPins, error) {
	p, ok := Pins[arch]
	if !ok {
		return ArchPins{}, fmt.Errorf("cloud hypervisor has no pinned runtime for %q. Try: use an amd64 or arm64 Linux host", arch)
	}
	return p, nil
}

// RuntimePaths names the on-disk paths of the pinned runtime artefacts on a host.
type RuntimePaths struct {
	Dir      string
	VMM      string
	CHRemote string
	Kernel   string
}

// DefaultRuntimePaths returns the default runtime paths under paths.RuntimeArtefactsDir().
func DefaultRuntimePaths() RuntimePaths {
	return RuntimePathsIn(filepath.Join(paths.RuntimeArtefactsDir(), RuntimeLabel))
}

// RuntimePathsIn returns the runtime paths rooted at dir.
func RuntimePathsIn(dir string) RuntimePaths {
	return RuntimePaths{
		Dir:      dir,
		VMM:      filepath.Join(dir, "cloud-hypervisor"),
		CHRemote: filepath.Join(dir, "ch-remote"),
		Kernel:   filepath.Join(dir, "vmlinux"),
	}
}

// VerifyFileSHA256 verifies that path exists as a regular file and its SHA-256
// matches wantHex (case-insensitive).
func VerifyFileSHA256(path, wantHex string) error {
	got, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, wantHex) {
		return fmt.Errorf("%s SHA-256 is %s, want %s. Try: remove the corrupted runtime file and fetch it again", path, got, wantHex)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EnsureRuntime verifies or downloads the pinned cloud-hypervisor, ch-remote
// and Kata 6.18 guest kernel into dir for arch (defaulting to runtime.GOARCH).
// Every file is verified against its pinned SHA-256 before atomic install.
func EnsureRuntime(ctx context.Context, dir, arch string, hc *http.Client) (RuntimePaths, error) {
	if arch == "" {
		arch = runtime.GOARCH
	}
	pins, err := PinsForArch(arch)
	if err != nil {
		return RuntimePaths{}, err
	}
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return RuntimePaths{}, fmt.Errorf("create runtime directory: %w", err)
	}
	if hc == nil {
		hc = &http.Client{
			Timeout: 5 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing non-HTTPS redirect to %s", req.URL)
				}
				if len(via) >= 10 {
					return errors.New("too many redirects while downloading runtime")
				}
				return nil
			},
		}
	}
	rp := RuntimePathsIn(dir)
	if err := ensurePinnedBinary(ctx, hc, rp.VMM, pins.VMM); err != nil {
		return RuntimePaths{}, err
	}
	if err := ensurePinnedBinary(ctx, hc, rp.CHRemote, pins.CHRemote); err != nil {
		return RuntimePaths{}, err
	}
	if err := ensurePinnedKernel(ctx, hc, rp.Kernel, pins.Kernel); err != nil {
		return RuntimePaths{}, err
	}
	return rp, nil
}

func ensurePinnedBinary(ctx context.Context, hc *http.Client, dst string, pin FilePin) error {
	if VerifyFileSHA256(dst, pin.SHA256) == nil {
		return nil
	}
	tmp, err := downloadVerified(ctx, hc, filepath.Dir(dst), pin)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	//nolint:gosec // G302: executable runtime binary (cloud-hypervisor / ch-remote)
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("install %s: %w", dst, err)
	}
	cleanup = false
	return nil
}

func ensurePinnedKernel(ctx context.Context, hc *http.Client, dst string, pin KernelPin) error {
	if VerifyFileSHA256(dst, pin.KernelSHA256) == nil {
		return nil
	}
	dir := filepath.Dir(dst)
	archiveTmp, err := downloadVerified(ctx, hc, dir, pin.Archive)
	if err != nil {
		return err
	}
	defer os.Remove(archiveTmp)

	extractDir, err := os.MkdirTemp(dir, ".kernel-extract-*")
	if err != nil {
		return fmt.Errorf("create kernel extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	out, err := exec.CommandContext(ctx, "tar", "--zstd", "-xf", archiveTmp, "-C", extractDir, pin.ArchiveMember).CombinedOutput()
	if err != nil {
		return fmt.Errorf("extract kernel from Kata substrate (%s): %w. Try: ensure tar and zstd are installed", strings.TrimSpace(string(out)), err)
	}
	extracted := filepath.Join(extractDir, filepath.FromSlash(strings.TrimPrefix(pin.ArchiveMember, "./")))
	if err := VerifyFileSHA256(extracted, pin.KernelSHA256); err != nil {
		return err
	}
	//nolint:gosec // G302: read-only guest kernel image, mode 0444
	if err := os.Chmod(extracted, 0o444); err != nil {
		return fmt.Errorf("chmod kernel: %w", err)
	}
	if err := os.Rename(extracted, dst); err != nil {
		return fmt.Errorf("install kernel: %w", err)
	}
	return nil
}

func downloadVerified(ctx context.Context, hc *http.Client, dir string, pin FilePin) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w. Try: check network access to GitHub releases", pin.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s returned HTTP %d. Try: check the pinned release URL", pin.URL, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, ".fetch-*")
	if err != nil {
		return "", fmt.Errorf("create temp download file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	const maxBytes = 256 << 20 // 256 MiB safety cap
	limit := pin.Size
	if limit <= 0 {
		limit = maxBytes
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, limit+1))
	closeErr := tmp.Close()
	if err != nil {
		return "", fmt.Errorf("write %s: %w", pin.URL, err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close %s: %w", tmpPath, closeErr)
	}
	if pin.Size > 0 && n != pin.Size {
		return "", fmt.Errorf("download %s size %d differs from pinned %d. Try: retry the download", pin.URL, n, pin.Size)
	}
	if n > maxBytes {
		return "", errors.New("downloaded artefact exceeds 256 MiB cap. Try: check guest/supply.lock")
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, pin.SHA256) {
		return "", fmt.Errorf("download %s SHA-256 %s differs from pinned %s. Try: retry or check upstream release integrity", pin.URL, got, pin.SHA256)
	}
	cleanup = false
	return tmpPath, nil
}
