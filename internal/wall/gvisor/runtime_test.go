package gvisor

import (
	"errors"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func runtimeManifest(t *testing.T) supply.Manifest {
	t.Helper()
	m := supply.Manifest{Release: "v0.0.0", Runtime: "gvisor-fixture", Advisories: []supply.Advisory{}}
	for _, name := range requiredRoles {
		item := supply.NewItem(supply.KindGvisor, name, NativeBuildVersionV1, runtime.GOARCH, []byte("fixture "+name), brand.ReleasesURL+"/download/v0.0.0/"+name)
		item.Released = "2026-11-02"
		m.Items = append(m.Items, item)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRuntimeRequiresOriginalSameBuildCompanionClosure(t *testing.T) {
	m := runtimeManifest(t)
	items, err := RuntimeItemsV1(m, runtime.GOARCH)
	if err != nil || len(items) != 5 {
		t.Fatal("complete samebuild closure refused", items, err)
	}
	items[0].Version = "changed"
	if m.Items[0].Version == "changed" {
		t.Fatal("selector borrowed mutable original item storage")
	}
	for _, index := range []int{1, 2, 3, 4} {
		copy := m
		copy.Items = append([]supply.Item(nil), m.Items...)
		copy.Items = append(copy.Items[:index], copy.Items[index+1:]...)
		if _, err := RuntimeItemsV1(copy, runtime.GOARCH); err == nil {
			t.Fatal("missing companion accepted", index)
		}
	}
	copy := m
	copy.Items = append([]supply.Item(nil), m.Items...)
	copy.Items[1].Version = "release-20261102.0-5e6f7a8b"
	if _, err := RuntimeItemsV1(copy, runtime.GOARCH); err == nil {
		t.Fatal("mixed signed builds accepted")
	}
	copy.Items[1] = m.Items[1]
	copy.Items[1].Released = "2026-11-03"
	if _, err := RuntimeItemsV1(copy, runtime.GOARCH); err == nil {
		t.Fatal("mixed release dates accepted")
	}
}

func TestRunscAdvisoryCoversDownstreamSuffixAndWholeLaunch(t *testing.T) {
	m := runtimeManifest(t)
	m.Advisories = []supply.Advisory{{ID: "GHSA-aaaa-bbbb-cccc", Kind: supply.KindGvisor, Name: "runsc", Before: "release-20261103.0", Severity: "high", CVSS: 8, Published: "2026-11-02", URL: "https://github.com/google/gvisor/security/advisories/GHSA-aaaa-bbbb-cccc"}}
	_, err := RuntimeItemsV1(m, runtime.GOARCH)
	var failure *proto.Error
	if !errors.As(err, &failure) || failure.Code != "runtime_advisory" {
		t.Fatal("downstream suffix evaded runsc advisory", err)
	}
}

func TestGvisorRuntimeDoesNotAcceptOtherArchitecture(t *testing.T) {
	m := runtimeManifest(t)
	other := "arm64"
	if runtime.GOARCH == other {
		other = "amd64"
	}
	if _, err := RuntimeItemsV1(m, other); err == nil {
		t.Fatal("wrong architecture supplied runtime closure")
	}
}

func TestGvisorRuntimeRefusesUnpatchedAndUnrelatedDistributionBuilds(t *testing.T) {
	for _, version := range []string{"release-20261102.0", "release-20261102.0-1a2b3c4d", "release-20261102.0-dea09068", "release-20261102.0-5fbc10e2", "release-20261103.0"} {
		manifest := runtimeManifest(t)
		for index := range manifest.Items {
			manifest.Items[index].Version = version
		}
		if _, err := RuntimeItemsV1(manifest, runtime.GOARCH); err == nil {
			t.Fatal("a release label supplied the missing patched native contract", version)
		}
	}
}
