package chv

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"time"
)

// InstallOptions configures InstallRuntime for `wall install microvm`.
type InstallOptions struct {
	Dir        string       // destination runtime directory; defaults to DefaultRuntimePaths().Dir
	Arch       string       // target arch ("amd64" or "arm64"); defaults to runtime.GOARCH
	HelperBin  string       // static Linux host helper binary used as /init in tools.erofs
	ShadowVT   string       // optional shadow-vt binary placed at /bin/<VTName> in tools.erofs
	VTName     string       // basename under /bin/ when ShadowVT is set
	MkfsErofs  string       // optional override for mkfs.erofs path (tests)
	HTTPClient *http.Client // optional HTTP client (tests)
}

// InstallResult summarises an installed and verified microvm runtime bundle.
type InstallResult struct {
	Dir           string
	Paths         RuntimePaths
	Tools         *ToolsRecord
	Arch          string
	VMMVersion    string
	KernelVersion string
}

// InstallRuntime downloads and SHA-256 verifies the pinned Cloud Hypervisor
// VMM, ch-remote and Kata 6.18 guest kernel from guest/supply.lock, builds and
// verifies the dm-verity tools image when opts.HelperBin is set (using
// mkfs.erofs when installed or pure-Go hcsshim tar2ext4 otherwise), and writes
// a runtime receipt JSON beside the runtime directory so host capability probes
// list the installed microvm runtime.
func InstallRuntime(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	dir := opts.Dir
	if dir == "" {
		dir = DefaultRuntimePaths().Dir
	}
	arch := opts.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	pins, err := PinsForArch(arch)
	if err != nil {
		return InstallResult{}, err
	}

	rp, err := EnsureRuntime(ctx, dir, arch, opts.HTTPClient)
	if err != nil {
		return InstallResult{}, err
	}

	var toolsRec *ToolsRecord
	if opts.HelperBin != "" {
		rec, err := EnsureToolsImage(ctx, opts.HelperBin, rp.Tools, ToolsBuildOptions{
			MkfsErofs:         opts.MkfsErofs,
			AllowExt4Fallback: true,
			ShadowVT:          opts.ShadowVT,
			VTName:            opts.VTName,
		})
		if err != nil {
			return InstallResult{}, err
		}
		toolsRec = &rec
	}

	if err := writeRuntimeReceipt(dir, arch, pins, toolsRec); err != nil {
		return InstallResult{}, err
	}

	return InstallResult{
		Dir:           dir,
		Paths:         rp,
		Tools:         toolsRec,
		Arch:          arch,
		VMMVersion:    PinnedVMMVersion,
		KernelVersion: PinnedKernelVersion,
	}, nil
}

func writeRuntimeReceipt(dir, arch string, pins ArchPins, tools *ToolsRecord) error {
	label := filepath.Base(filepath.Clean(dir))
	doc := struct {
		Kind          string       `json:"kind"`
		Runtime       string       `json:"runtime"`
		Arch          string       `json:"arch"`
		VMMVersion    string       `json:"vmm_version"`
		KernelVersion string       `json:"kernel_version"`
		RecordedAt    time.Time    `json:"recorded_at"`
		VMMSHA256     string       `json:"vmm_sha256"`
		CHRemoteSHA   string       `json:"ch_remote_sha256"`
		KernelSHA256  string       `json:"kernel_sha256"`
		Tools         *ToolsRecord `json:"tools,omitempty"`
	}{
		Kind:          DriverName,
		Runtime:       label,
		Arch:          arch,
		VMMVersion:    PinnedVMMVersion,
		KernelVersion: PinnedKernelVersion,
		RecordedAt:    time.Now().UTC(),
		VMMSHA256:     pins.VMM.SHA256,
		CHRemoteSHA:   pins.CHRemote.SHA256,
		KernelSHA256:  pins.Kernel.KernelSHA256,
		Tools:         tools,
	}
	receiptPath := filepath.Join(filepath.Dir(filepath.Clean(dir)), label+".json")
	if err := writeJSONAtomic(receiptPath, doc); err != nil {
		return fmt.Errorf("write microvm runtime receipt: %w", err)
	}
	return nil
}
