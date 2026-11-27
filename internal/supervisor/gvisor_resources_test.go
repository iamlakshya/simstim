package supervisor

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

func TestGvisorFactoryRejectsManifestAndExpectedSpecWithoutNativeReceipt(t *testing.T) {
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, t.TempDir())
	}
	selected := supply.Manifest{Release: "v0.0.0", Runtime: "gvisor-fixture", Advisories: []supply.Advisory{}}
	for _, role := range []string{"runsc", "gvisor_sentry", "gvisor-sentry-prewarmer", "gvisor-proc-umounter"} {
		item := supply.NewItem(supply.KindGvisor, role, gvisor.NativeBuildVersionV1, runtime.GOARCH, []byte(role), brand.ReleasesURL+"/download/v0.0.0/"+role)
		item.Released = "2026-11-02"
		selected.Items = append(selected.Items, item)
	}
	if err := selected.Validate(); err != nil {
		t.Fatal(err)
	}
	m := &host.Manifest{ID: "abcdef23", Wall: &host.WallRecord{Level: "gvisor", Driver: "gvisor"}, RuntimeManifest: &selected}
	m.Limits.MemoryMax = 4 << 30
	plan, err := host.SandboxStatePlan(m)
	if err != nil {
		t.Fatal(err)
	}
	owner := wall.WithExpectedSpec(t.Context(), wall.Spec{ID: m.ID, SessionDir: paths.SessionDir(m.ID), RuntimeManifest: &selected, MemoryMax: m.Limits.MemoryMax, StateDirs: plan.Dirs, StateFiles: plan.Files})
	for _, ctx := range []context.Context{t.Context(), owner} {
		if _, err := prepareGVisor(ctx, m); err == nil {
			t.Fatal("raw runtime and matching expected data entered the gvisor factory")
		}
		if _, err := os.Stat(paths.SessionDir(m.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unissued metadata prepared a filesystem or capture", err)
		}
	}
	cancelled, cancel := context.WithCancel(owner)
	cancel()
	if _, err := prepareGVisor(cancelled, m); !errors.Is(err, context.Canceled) {
		t.Fatal("preparation lost its original cancellation", err)
	}
}
