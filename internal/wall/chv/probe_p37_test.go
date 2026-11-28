package chv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestProbeP37ArtefactStoreDigestVirtAndLimaARM64(t *testing.T) {
	ctx := context.Background()
	fakeReg := fakeFileInfo{name: "f"}
	amd64Pins, err := PinsForArch("amd64")
	if err != nil {
		t.Fatal(err)
	}
	arm64Pins, err := PinsForArch("arm64")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("finds pinned runtime in H9 artefact store and verifies SHA-256", func(t *testing.T) {
		storeDir := "/var/lib/artefacts/sha256"
		vmmPath := filepath.Join(storeDir, amd64Pins.VMM.SHA256)
		remPath := filepath.Join(storeDir, amd64Pins.CHRemote.SHA256)
		kernPath := filepath.Join(storeDir, amd64Pins.Kernel.KernelSHA256)

		verifiedPath := ""
		p := ProbeWith(ctx, ProbeDeps{
			GOOS:          "linux",
			GOARCH:        "amd64",
			KernelRelease: func() string { return "6.12.118+deb13-cloud-amd64" },
			Stat: func(path string) (os.FileInfo, error) {
				switch path {
				case devKVMPath, "/sbin/mkfs.ext4", vmmPath, remPath, kernPath:
					return fakeReg, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			CheckKVMRW:   func(string) error { return nil },
			LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
			RuntimePaths: func() RuntimePaths { return RuntimePathsIn("/nonexistent/runtime") },
			ArtefactFile: func(hex string) string { return filepath.Join(storeDir, hex) },
			VerifySHA256: func(path, want string) error {
				verifiedPath = path
				if want != amd64Pins.VMM.SHA256 {
					return errors.New("unexpected sha")
				}
				return nil
			},
			DetectVirt: func(context.Context) string { return "kvm" },
			RunCmd: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name != vmmPath {
					return nil, errors.New("wrong vmm path: " + name)
				}
				return []byte("cloud-hypervisor v55.0\n"), nil
			},
		})
		if !p.OK || p.Level != wall.MicroVM || p.Version != "55.0" {
			t.Fatalf("probe = %+v", p)
		}
		if verifiedPath != vmmPath {
			t.Errorf("verifiedPath = %q, want %q", verifiedPath, vmmPath)
		}
		if p.Facts["virt"] != "kvm" || p.Facts["kvm_api"] != "12" || p.Facts["vmm_sha256"] != amd64Pins.VMM.SHA256 {
			t.Errorf("facts = %+v", p.Facts)
		}
	})

	t.Run("fails closed when VMM SHA-256 verification fails", func(t *testing.T) {
		rp := RuntimePathsIn("/opt/runtime")
		p := ProbeWith(ctx, ProbeDeps{
			GOOS:          "linux",
			GOARCH:        "amd64",
			KernelRelease: func() string { return "6.12.118+deb13-cloud-amd64" },
			Stat: func(path string) (os.FileInfo, error) {
				switch path {
				case devKVMPath, "/sbin/mkfs.ext4", rp.VMM, rp.CHRemote, rp.Kernel:
					return fakeReg, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			CheckKVMRW:   func(string) error { return nil },
			LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
			RuntimePaths: func() RuntimePaths { return rp },
			VerifySHA256: func(string, string) error { return errors.New("SHA-256 mismatch") },
			DetectVirt:   func(context.Context) string { return "none" },
		})
		if p.OK || !strings.Contains(p.Reason, "SHA-256 verification") || p.Facts["vmm"] != "digest_mismatch" {
			t.Fatalf("digest mismatch probe = %+v", p)
		}
		if _, hasVirt := p.Facts["virt"]; hasVirt {
			t.Errorf("virt=none should be omitted from facts: %+v", p.Facts)
		}
	})

	t.Run("KVM_GET_API_VERSION failure reports unusable kvm", func(t *testing.T) {
		p := ProbeWith(ctx, ProbeDeps{
			GOOS:          "linux",
			GOARCH:        "amd64",
			KernelRelease: func() string { return "6.12.118+deb13-cloud-amd64" },
			Stat: func(path string) (os.FileInfo, error) {
				if path == devKVMPath {
					return fakeReg, nil
				}
				return nil, os.ErrNotExist
			},
			CheckKVMRW:  func(string) error { return errors.New("KVM_GET_API_VERSION returned 11, want 12") },
			CurrentUser: func() string { return "admin" },
			LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		})
		if p.OK || p.Facts["kvm_usable"] != "false" || !strings.Contains(p.Reason, "KVM_GET_API_VERSION") || p.Fix != "sudo usermod -aG kvm admin" {
			t.Fatalf("kvm api error probe = %+v", p)
		}
	})

	t.Run("Lima arm64 VM on Apple M3+ with macOS 15+ probes microvm OK", func(t *testing.T) {
		storeDir := "/var/lib/artefacts/sha256"
		vmmPath := filepath.Join(storeDir, arm64Pins.VMM.SHA256)
		remPath := filepath.Join(storeDir, arm64Pins.CHRemote.SHA256)
		kernPath := filepath.Join(storeDir, arm64Pins.Kernel.KernelSHA256)
		p := ProbeWith(ctx, ProbeDeps{
			GOOS:          "linux",
			GOARCH:        "arm64",
			KernelRelease: func() string { return "6.8.0-149-generic" },
			Stat: func(path string) (os.FileInfo, error) {
				switch path {
				case devKVMPath, "/usr/sbin/mkfs.ext4", vmmPath, remPath, kernPath:
					return fakeReg, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			CheckKVMRW:   func(string) error { return nil },
			LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
			RuntimePaths: func() RuntimePaths { return RuntimePathsIn("/nonexistent/runtime") },
			ArtefactFile: func(hex string) string { return filepath.Join(storeDir, hex) },
			VerifySHA256: func(string, string) error { return nil },
			DetectVirt:   func(context.Context) string { return "apple" },
			RunCmd: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte("cloud-hypervisor v55.0\n"), nil
			},
		})
		if !p.OK || p.Driver != DriverName || p.Level != wall.MicroVM || p.Facts["virt"] != "apple" {
			t.Fatalf("Lima arm64 probe = %+v", p)
		}
	})
}
