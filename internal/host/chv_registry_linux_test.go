//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

type vmFactoryDriver struct{ prepared, opened, built wall.Spec }

func (*vmFactoryDriver) Name() string      { return "chv" }
func (*vmFactoryDriver) Level() wall.Level { return wall.MicroVM }
func (*vmFactoryDriver) Probe(context.Context) wall.Probe {
	panic("factory must not use readiness as first-boot interlock")
}
func (d *vmFactoryDriver) Prepare(_ context.Context, spec wall.Spec) (wall.Sandbox, error) {
	d.prepared = spec
	return nil, nil
}
func (d *vmFactoryDriver) Open(ctx context.Context, _ string) (wall.Sandbox, error) {
	d.opened, _ = wall.ExpectedSpec(ctx)
	return nil, nil
}
func (d *vmFactoryDriver) Build(_ context.Context, spec wall.Spec) (wall.Sandbox, error) {
	d.built = spec
	return nil, nil
}

func TestCloudHypervisorIsActuallyRegistered(t *testing.T) {
	count := 0
	for _, driver := range wall.Drivers() {
		if driver.Name() == "chv" {
			count++
			if driver != &cloudHypervisorDriver || driver.Level() != wall.MicroVM {
				t.Fatal("registry contains a diagnostic substitute")
			}
		}
	}
	if count != 1 || CloudHypervisorResourcesV1().Version() != 1 {
		t.Fatal("actual version1 registry/factory join is missing")
	}
}

func TestVMFactoryRequiresFreshExpectedSessionBeforeDispatch(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	driver := &vmFactoryDriver{}
	factory := &vmResourcesV1{driver: driver}
	if _, err := factory.Prepare(context.Background()); err == nil {
		t.Fatal("factory accepted missing client authority")
	}
	spec := wall.Spec{ID: "abcdef23", SessionDir: paths.SessionDir("abcdef23"), VM: &wall.VMSpec{Version: 1}}
	ctx := wall.WithExpectedSpec(context.Background(), spec)
	if _, err := factory.Prepare(ctx); err != nil || driver.prepared.ID != spec.ID {
		t.Fatalf("Prepare did not reach actual driver: %v", err)
	}
	if _, err := factory.Build(ctx); err != nil || driver.built.ID != spec.ID {
		t.Fatalf("Build did not dispatch to actual version1 driver: %v", err)
	}
	if _, err := factory.Open(ctx, paths.SessionDir("bcdef234")); err == nil {
		t.Fatal("factory dispatched a different saved session")
	}
	if _, err := factory.Open(ctx, spec.SessionDir); err != nil || driver.opened.ID != spec.ID {
		t.Fatalf("Open lost fresh expected authority: %v", err)
	}
}
