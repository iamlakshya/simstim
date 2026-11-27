//go:build linux

package gvisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestPhysicalProbeReturnsActualPreCommandStorageOwner(t *testing.T) {
	if !ConstructionProbeV1(t.Context()).CanPrepare {
		t.Skip("this kernel has no construction support")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gvisor"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := runtimeManifest(t)
	driver := &Driver{options: Options{ExpectedRuntime: func(context.Context) (supply.Manifest, error) { return manifest, nil }, Domain: func(context.Context) (cachefs.Domain, error) { return cachefs.Domain{}, nil }}}
	spec := wall.Spec{ID: "gvisorprobe", SessionDir: dir, Helper: t.TempDir(), Root: wall.Reset, MemoryMax: 512 << 20, CPUs: 1}
	probe, resource := driver.ProbeOwnedV1(wall.WithExpectedSpec(t.Context(), spec))
	if probe.OK || resource == nil {
		t.Fatal("failed pre-command factory discarded its actual resource", probe, resource)
	}
	actual := resource.(*sandbox)
	if actual.storage == nil || actual.storage.session == nil || actual.storage.lease == nil {
		t.Fatal("actual partial storage owner was not returned")
	}
	if err := actual.ReleasePreparedV1(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gvisor")); err != nil {
		t.Fatal("metadata release removed an unowned replacement", err)
	}
}
