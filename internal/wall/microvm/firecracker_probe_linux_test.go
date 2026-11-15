//go:build linux

package microvm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestFirecrackerProbeRefusesUnsupportedHostBeforeRuntimeAccess(t *testing.T) {
	for name, test := range map[string]struct {
		facts  api.CapProbe
		reason string
	}{
		"operating system": {api.CapProbe{OS: "darwin", Arch: "arm64", Kernel: "6.18.1"}, "needs Linux"},
		"architecture":     {api.CapProbe{OS: "linux", Arch: "other", Kernel: "6.18.1"}, "needs Linux"},
		"old kernel":       {api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "5.4.1"}, "host kernel"},
		"untested series":  {api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "6.12.1"}, "host kernel"},
		"5.10 needs KVM":   {api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "5.10.1"}, "/dev/kvm"},
		"6.1 needs KVM":    {api.CapProbe{OS: "linux", Arch: "arm64", Kernel: "6.1.1"}, "/dev/kvm"},
		"6.18 needs KVM":   {api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "6.18.1"}, "/dev/kvm"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := firecrackerFixture(t)
			options := fixtureDriver(t, spec.SessionDir).options
			options.Facts = func(context.Context) (api.CapProbe, error) { return test.facts, nil }
			options.Domain = func(context.Context) (cachefs.Domain, error) {
				t.Fatal("a failed prerequisite reached runtime storage")
				return cachefs.Domain{}, nil
			}
			driver, err := NewForDriver("firecracker", options)
			if err != nil {
				t.Fatal(err)
			}
			probe := driver.Probe(t.Context())
			if probe.OK || probe.CanPrepare || !strings.Contains(probe.Reason, test.reason) || probe.Driver != "firecracker" {
				t.Fatal("an unsupported host acquired VM readiness", probe)
			}
			if _, err := os.Stat(options.Evidence); !os.IsNotExist(err) {
				t.Fatal("a failed prerequisite created boot evidence", err)
			}
		})
	}
}

func TestFirecrackerVersionFloorRefusesBeforePersistentLayout(t *testing.T) {
	for _, version := range []string{"1.16.1", "1.17.0-rc.1", "", "invalid"} {
		t.Run(version, func(t *testing.T) {
			spec := firecrackerFixture(t)
			spec.VM.Runtime.Items[0].Version = version
			driver, err := NewForDriver("firecracker", fixtureDriver(t, spec.SessionDir).options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Prepare(t.Context(), spec); err == nil {
				t.Fatal("an unsafe or unknown VMM version reached preparation", version)
			}
			if _, err := os.Stat(filepath.Join(spec.SessionDir, "firecracker.json")); !os.IsNotExist(err) {
				t.Fatal("a refused runtime changed persistent layout", err)
			}
		})
	}
	for _, version := range []string{"1.17.0", "1.18.0"} {
		spec := firecrackerFixture(t)
		spec.VM.Runtime.Items[0].Version = version
		driver, err := NewForDriver("firecracker", fixtureDriver(t, spec.SessionDir).options)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := driver.Prepare(t.Context(), spec)
		if err != nil {
			t.Fatal("the supported floor refused a valid plan", version, err)
		}
		if err := prepared.(*sandbox).ReleasePreparedV1(); err != nil {
			t.Fatal(err)
		}
	}
}
