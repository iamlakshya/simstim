//go:build linux

package host

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

func probeRuntimeFixtureV1(t *testing.T) supply.Manifest {
	t.Helper()
	manifest := supply.Manifest{Release: "v0.0.0", Runtime: "probe-fixture", Advisories: []supply.Advisory{}}
	for _, name := range []string{"runsc", "gvisor_sentry", "gvisor-sentry-prewarmer", "gvisor-proc-umounter", "runsc-fd-parking"} {
		item := supply.NewItem(supply.KindGvisor, name, gvisor.NativeBuildVersionV1, runtime.GOARCH, []byte("fixture "+name), brand.ReleasesURL+"/download/v0.0.0/"+name)
		item.Released = "2026-11-02"
		manifest.Items = append(manifest.Items, item)
	}
	if _, err := gvisor.RuntimeItemsV1(manifest, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestPhysicalProbeRequiresActualSameBridgeDeliveryBeforeEffects(t *testing.T) {
	manifest := probeRuntimeFixtureV1(t)
	b := &bridge{ctx: context.Background()}
	args, err := json.Marshal(sandboxProbeArgsV1{Runtime: &manifest})
	if err != nil {
		t.Fatal(err)
	}
	ctx, finish, err := b.gvisorProbeContextV1(proto.Request{ID: 1, Op: "sandbox.probe", Args: args}, manifest)
	if err == nil || ctx != nil || finish != nil {
		t.Fatal("a raw manifest issued a physical native probe purpose")
	}
	if len(b.runtime.selections) != 0 {
		t.Fatal("a probe manufactured the delivery receipt it was meant to consume")
	}
}

func TestCopiedProbeDataAndExpectedSpecCannotIssueNativeRuntime(t *testing.T) {
	manifest := probeRuntimeFixtureV1(t)
	purpose := &gvisorProbePurposeV1{selected: &selectedRuntimeReceiptV1{manifest: manifest}, request: 1}
	ctx := context.WithValue(context.Background(), gvisorProbeKeyV1{}, purpose)
	ctx = wall.WithExpectedSpec(ctx, wall.Spec{ID: "gvisorprobe", RuntimeManifest: &manifest})
	if _, err := gvisorExpectedRuntimeV1(ctx); err == nil {
		t.Fatal("copied probe data supplied a missing native delivery owner")
	}
	if _, err := gvisorNativeDomainV1(ctx); err == nil {
		t.Fatal("copied probe data supplied a missing native host domain")
	}
	if OriginalVMNeedFromContext(ctx) != nil {
		t.Fatal("a diagnostic purpose became a session boot token")
	}
}

func TestGvisorRuntimeCompatibilityDoesNotSupplyPhysicalOrBootAuthority(t *testing.T) {
	registration := &gvisorRegistrationV1{}
	if _, ok := registration.RuntimeCompatibility(supply.Manifest{}); ok {
		t.Fatal("an empty inventory verified a runtime closure")
	}
	manifest := probeRuntimeFixtureV1(t)
	status, ok := registration.RuntimeCompatibility(manifest)
	if !ok || !status.Verified || status.Name != "gvisor" {
		t.Fatal("the complete selected closure lost its construction projection", status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if probe := registration.Probe(ctx); probe.OK {
		t.Fatal("runtime compatibility became physical readiness")
	}
	if _, err := ExpectedOriginalRuntime(context.Background()); err == nil {
		t.Fatal("a compatibility projection issued an original boot selection")
	}
}

func TestGvisorCachedFactRequiresTheExactOriginalRuntimeClosure(t *testing.T) {
	probe := api.WallProbe{Driver: "gvisor", Level: "gvisor", OK: true, Version: gvisor.NativeBuildVersionV1, Facts: map[string]string{"runtime_manifest": "actual-original", "native_closure_v1": "actual-native"}}
	if !cachedGvisorMatchesV1(probe, "actual-original", "actual-native") {
		t.Fatal("the exact physical selected-runtime fact was refused")
	}
	if cachedGvisorMatchesV1(probe, "other", "actual-native") || cachedGvisorMatchesV1(probe, "", "actual-native") {
		t.Fatal("a cached fact chose or invented a runtime selection")
	}
	if cachedGvisorMatchesV1(probe, "actual-original", "changed-C-only") || cachedGvisorMatchesV1(probe, "actual-original", "") {
		t.Fatal("C-only/unknown closure reused physical readiness")
	}
	delete(probe.Facts, "native_closure_v1")
	if cachedGvisorMatchesV1(probe, "actual-original", "actual-native") {
		t.Fatal("old runtime-only cache became complete native proof")
	}
	probe.Facts["native_closure_v1"] = "actual-native"
	probe.OK = false
	if cachedGvisorMatchesV1(probe, "actual-original", "actual-native") {
		t.Fatal("construction-only data became physical readiness")
	}
}
