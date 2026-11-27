package none_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

func TestNoneDriverHasNoNamespaceOrDiskPlan(t *testing.T) {
	driver := none.Driver{}
	probe := driver.Probe(context.Background())
	if driver.Name() != "none" || driver.Level() != wall.None || probe.OK != (runtime.GOOS == "linux") {
		t.Fatal("none did not probe as the unwalled runtime")
	}
	if runtime.GOOS != "linux" {
		if probe.CanPrepare || probe.Reason == "" {
			t.Fatal("a client platform claimed production None host support")
		}
		if _, err := driver.Prepare(context.Background(), wall.Spec{}); err == nil {
			t.Fatal("an unsupported host platform started the new None path")
		}
		return
	}
	base := t.TempDir()
	directory := filepath.Join(base, "a2a2a2a2")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := driver.Prepare(context.Background(), wall.Spec{ID: "a2a2a2a2", SessionDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Abilities().Freeze || runtime.Abilities().Reflink {
		t.Fatal("none claimed sandbox snapshot abilities")
	}
	if err := runtime.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatal("none destroyed the existing session or working directory")
	}
	if _, err := driver.Prepare(context.Background(), wall.Spec{ID: "a2a2a2a2", SessionDir: directory, VM: &wall.VMSpec{}}); err == nil {
		t.Fatal("none accepted a VM plan")
	}
	if _, err := none.NativeLiveOwnerV1(nil, nil); err == nil {
		t.Fatal("data minted a native unwalled runtime owner")
	}
}
