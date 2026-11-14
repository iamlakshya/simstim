//go:build linux

package microvm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBootEvidenceRequiresExactRuntimeHostAndIdentity(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	facts := api.CapProbe{Arch: spec.VM.Image.Arch, Kernel: "6.18.63", Subuid: true, KVM: api.KVM{InGroupManager: true}}
	if driver.checkBootEvidence(spec.VM.Runtime, facts) == nil {
		t.Fatal("downloaded files alone counted as a successful boot")
	}
	if err := driver.recordBoot(context.Background(), spec.VM.Runtime, facts); err != nil {
		t.Fatal(err)
	}
	if err := driver.checkBootEvidence(spec.VM.Runtime, facts); err != nil {
		t.Fatal(err)
	}
	previousIdentity := driver.options.Identity
	driver.options.Identity = func() (string, error) { return "new-host-boot-or-helper-bytes", nil }
	if driver.checkBootEvidence(spec.VM.Runtime, facts) == nil {
		t.Fatal("new helper bytes or host boot inherited an old boot proof")
	}
	driver.options.Identity = previousIdentity
	for name, change := range map[string]func(){
		"host kernel":   func() { facts.Kernel = "6.18.64" },
		"uid mode":      func() { facts.Subuid = false },
		"runtime bytes": func() { spec.VM.Runtime.Items[0].SHA256 = spec.VM.Runtime.Items[1].SHA256 },
		"tools root":    func() { spec.VM.Runtime.Items[2].RootHash = spec.VM.Runtime.Items[0].SHA256 },
	} {
		t.Run(name, func(t *testing.T) {
			oldFacts := facts
			oldItems := slices.Clone(spec.VM.Runtime.Items)
			change()
			if driver.checkBootEvidence(spec.VM.Runtime, facts) == nil {
				t.Fatal("stale boot evidence accepted")
			}
			facts = oldFacts
			spec.VM.Runtime.Items = oldItems
		})
	}
}

func TestBootEvidenceOrderAndStrictSchema(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	facts := api.CapProbe{Arch: spec.VM.Image.Arch, Kernel: "6.18.63"}
	key, err := evidenceKey(spec.VM.Runtime, facts, "test-boot-helper")
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(spec.VM.Runtime.Items)
	reordered, err := evidenceKey(spec.VM.Runtime, facts, "test-boot-helper")
	if err != nil || key != reordered {
		t.Fatalf("input order changed boot identity: %s %s %v", key, reordered, err)
	}
	if err := driver.recordBoot(context.Background(), spec.VM.Runtime, facts); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(driver.options.Evidence, key+".json")
	for _, data := range []string{`{"version":2}`, `{"version":1,"key":"wrong","booted":"2026-11-01T00:00:00Z"}`, `{"version":1,"unexpected":true}`, `{}` + `{}`} {
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if driver.checkBootEvidence(spec.VM.Runtime, facts) == nil {
			t.Fatal("malformed host boot evidence accepted")
		}
	}
}
