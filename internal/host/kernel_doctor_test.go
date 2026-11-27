//go:build !windows

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func kernelFakeSourceV1(t *testing.T, files map[string]string) capSource {
	t.Helper()
	isolateCapTest(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		path := filepath.Join(root, strings.TrimPrefix(name, "/"))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return capSource{root: root, os: "linux", run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no fixture command") }}
}

func TestKernelDoctorUsesRecordedDebianPackageWithoutVersionSignature(t *testing.T) {
	files := map[string]string{}
	for name, target := range map[string]string{"debian13-uname-r": "/proc/sys/kernel/osrelease", "debian13-uname-v": "/proc/sys/kernel/version", "debian13-os-release": "/etc/os-release"} {
		raw, err := os.ReadFile(filepath.Join("testdata/kernel", name))
		if err != nil {
			t.Fatal(err)
		}
		files[target] = string(raw)
	}
	source := kernelFakeSourceV1(t, files)
	results, checks := kernelBugReportV1(t.Context(), source, "", nil)
	if len(results) != 2 || len(checks) != 0 {
		t.Fatal(results, checks)
	}
	for _, result := range results {
		if result.Result != "fixed" || result.Package != "6.12.117-1" {
			t.Fatal("recorded running package was not used", result)
		}
	}
}

func TestKernelDoctorFakeFilesWarnAndNeverRunMitigation(t *testing.T) {
	source := kernelFakeSourceV1(t, map[string]string{"/proc/sys/kernel/osrelease": "6.12.48-amd64\n", "/proc/sys/kernel/version": "#1 SMP Debian 6.12.48-1 (format fixture)\n", "/etc/os-release": "ID=debian\nVERSION_ID=13\n", "/sys/module/vhost_vsock/present": "loaded"})
	before, _ := os.ReadFile(source.path("/proc/sys/kernel/version"))
	results, checks := kernelBugReportV1(t.Context(), source, "", nil)
	if len(results) != 2 || len(checks) != 2 {
		t.Fatal(results, checks)
	}
	for _, result := range results {
		if result.Result != "affected" || result.Severity != "warn" {
			t.Fatal(result)
		}
	}
	after, _ := os.ReadFile(source.path("/proc/sys/kernel/version"))
	if string(after) != string(before) {
		t.Fatal("doctor changed its kernel source")
	}
	data, err := json.Marshal(results)
	if err != nil || !strings.Contains(string(data), `"id":"CVE-2026-53365"`) {
		t.Fatal("JSON lost per-advisory identifiers", err)
	}
	if _, err := os.Stat(source.path("/etc/modprobe.d")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("doctor created a mitigation", err)
	}
}

func TestKernelDoctorDistinguishesAbsentModuleFromBrokenSymlink(t *testing.T) {
	source := kernelFakeSourceV1(t, map[string]string{"/proc/sys/kernel/osrelease": "6.8.12\n"})
	parent := source.path("/sys/module")

	// Missing /sys/module parent cannot establish module absence.
	results, _ := kernelBugReportV1(t.Context(), source, "", nil)
	if results[0].Result != "maybe" {
		t.Fatal("missing /sys/module parent became mitigated", results)
	}

	// Non-directory /sys/module cannot establish module absence.
	if err := os.MkdirAll(filepath.Dir(parent), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	results, _ = kernelBugReportV1(t.Context(), source, "", nil)
	if results[0].Result != "maybe" {
		t.Fatal("non-directory /sys/module became mitigated", results)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}

	// Observable /sys/module directory with absent entry establishes module absence.
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if os.Geteuid() != 0 {
		for _, mode := range []os.FileMode{0o000, 0o111} {
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			results, _ = kernelBugReportV1(t.Context(), source, "", nil)
			if results[0].Result != "maybe" {
				t.Fatal("unreadable /sys/module became mitigated", mode, results)
			}
		}
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	results, _ = kernelBugReportV1(t.Context(), source, "", nil)
	if results[0].Result != "mitigated" || results[1].Result != "maybe" {
		t.Fatal("observable absent module or empty-mitigation result wrong", results)
	}

	// Broken symlink at /sys/module/vhost_vsock must not count as absent.
	path := filepath.Join(parent, "vhost_vsock")
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	results, _ = kernelBugReportV1(t.Context(), source, "", nil)
	if results[0].Result != "maybe" {
		t.Fatal("broken module link became known absence", results)
	}
}

func TestKernelDoctorUnreadableVersionIsInformationalUnknown(t *testing.T) {
	source := kernelFakeSourceV1(t, nil)
	results, checks := kernelBugReportV1(t.Context(), source, "", nil)
	if len(results) != 2 || len(checks) != 1 || !checks[0].OK {
		t.Fatal(results, checks)
	}
	for _, result := range results {
		if result.Result != "unknown" {
			t.Fatal(result)
		}
	}
}

func TestKernelDoctorPrereleaseAndNonLinuxBoundaries(t *testing.T) {
	nonLinux := kernelFakeSourceV1(t, map[string]string{"/proc/sys/kernel/osrelease": "7.2.0-rc1\n"})
	nonLinux.os = "darwin"
	if results, checks := kernelBugReportV1(t.Context(), nonLinux, "", nil); results != nil || checks != nil {
		t.Fatal("non-linux reported kernel bugs", results, checks)
	}

	probe := api.CapProbe{Kernel: "7.2.0-rc1", Walls: []api.WallProbe{{Driver: "chv", Level: "microvm", OK: true}}}
	for _, release := range []string{"7.2.0-rc1", "7.2.0-rc4"} {
		source := kernelFakeSourceV1(t, map[string]string{"/proc/sys/kernel/osrelease": release + "\n"})
		results, checks := kernelBugReportV1(t.Context(), source, "", &probe)
		if len(results) != 2 || results[0].Result != "fixed" || results[1].Result != "maybe" || len(checks) != 1 || checks[0].OK {
			t.Fatal(release, results, checks)
		}
	}
	if !probe.Walls[0].OK || probe.Walls[0].Level != "microvm" {
		t.Fatal("doctor changed wall floor")
	}

	rc4 := kernelFakeSourceV1(t, map[string]string{"/proc/sys/kernel/osrelease": "7.1.0-rc4\n"})
	if results, checks := kernelBugReportV1(t.Context(), rc4, "", nil); len(results) != 2 || results[0].Result != "maybe" || results[1].Result != "maybe" || len(checks) != 2 {
		t.Fatal("7.1.0-rc4 missed VsockDrop or Vghost", results, checks)
	}
	rc5 := kernelFakeSourceV1(t, map[string]string{"/proc/sys/kernel/osrelease": "7.1.0-rc5\n"})
	if results, checks := kernelBugReportV1(t.Context(), rc5, "", nil); len(results) != 2 || results[0].Result != "fixed" || results[1].Result != "maybe" || len(checks) != 1 {
		t.Fatal("7.1.0-rc5 boundary wrong", results, checks)
	}
	final72 := kernelFakeSourceV1(t, map[string]string{"/proc/sys/kernel/osrelease": "7.2.0\n"})
	if results, checks := kernelBugReportV1(t.Context(), final72, "", nil); len(results) != 2 || results[0].Result != "fixed" || results[1].Result != "fixed" || len(checks) != 0 {
		t.Fatal("7.2.0 final reported warning", results, checks)
	}
}
