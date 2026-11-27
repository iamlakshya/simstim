//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestMultiVMMStoreProvidesOnlySelectedPrepareMetadata(t *testing.T) {
	spec := vmFixture(t)
	base := fixtureDriver(t, spec.SessionDir)
	full := installProbeRuntimeV1(t, base, true)
	before, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	facts := api.CapProbe{OS: "linux", Arch: runtime.GOARCH, Kernel: "6.18.63", KVM: api.KVM{Present: true, Usable: true}}
	for _, name := range []string{"chv", "firecracker"} {
		t.Run(name, func(t *testing.T) {
			driver, err := NewForDriver(name, base.options)
			if err != nil {
				t.Fatal(err)
			}
			driver.options.Facts = func(context.Context) (api.CapProbe, error) { return facts, nil }
			probe := driver.probeRuntimeV1(t.Context(), facts, wall.Probe{Driver: name, Level: wall.MicroVM, Facts: map[string]string{}})
			if !probe.CanPrepare || probe.OK || probe.Facts["runtime_verified"] != "true" {
				t.Fatalf("full store failed selected-role comparison or fabricated boot evidence: %+v", probe)
			}
			if _, ready := driver.RuntimeCompatibility(full); ready {
				t.Fatal("metadata/store fixtures became successful boot or native authority")
			}
		})
	}
	domain, err := base.options.Domain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store, err := runtimecache.OpenHostReadOnly(t.Context(), base.options.Artefacts, domain)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.RecordsContext(t.Context(), runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Manifest.Items) != len(full.Items) {
		t.Fatal("driver comparison laundered the full store collection into a selected replacement record")
	}
	after, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("driver comparison replaced the full store record")
	}
}
