//go:build linux

package supply

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type nativeModeFixtureV1 struct {
	root, artifacts, modes *os.Root
	file                   *os.File
	item                   Item
	dir                    string
}

func (f *nativeModeFixtureV1) Close() {
	f.file.Close()
	f.modes.Close()
	f.artifacts.Close()
	f.root.Close()
}

func modeNativeFixtureV1(t *testing.T, elf bool) *nativeModeFixtureV1 {
	t.Helper()
	parent := os.Getenv("FSVERITY_NATIVE_FIXTURE")
	if parent == "" {
		t.Skip("no admitted writable native fs-verity fixture; no mode execution proof claimed")
	}
	if !filepath.IsAbs(parent) {
		t.Fatal("native fixture must be absolute")
	}
	dir, err := os.MkdirTemp(parent, "mode-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, name := range []string{"sha256", "execmode"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("fixed actual native mode fixture bytes")
	if elf {
		path := os.Getenv("FSVERITY_VMM_ELF")
		if !filepath.IsAbs(path) {
			t.Skip("no admitted pinned native test ELF; no direct-ELF proof claimed")
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128<<20 {
			file.Close()
			t.Fatal("native ELF fixture has invalid type/size", err)
		}
		data, err = io.ReadAll(io.LimitReader(file, info.Size()+1))
		file.Close()
		if err != nil || int64(len(data)) != info.Size() {
			t.Fatal(err)
		}
	}
	item := NewItem(KindVMM, "native-fixture", "1.2.3", runtime.GOARCH, data, ReleaseFolder("1.2.3")+"/native-fixture")
	item.EOL, item.HostKernel, item.Released = "2027-01-06", ">=6.8", "2026-11-01"
	path := filepath.Join(dir, "sha256", item.SHA256)
	if err := os.WriteFile(path, data, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o555); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := InstallRuntimeVerity(t.Context(), item, file)
	if err != nil || !enabled {
		file.Close()
		t.Fatal("admitted filesystem did not enable actual verity", enabled, err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := cachefs.OpenPrivateChild(r, "sha256", false)
	if err != nil {
		t.Fatal(err)
	}
	m, err := cachefs.OpenPrivateChild(r, "execmode", false)
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeModeFixtureV1{root: r, artifacts: a, modes: m, file: file, item: item, dir: dir}
	t.Cleanup(f.Close)
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture-item.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *nativeModeFixtureV1) owner(t *testing.T) *VerifiedRuntime {
	t.Helper()
	owner, err := PrepareRuntimeWithModeV1(t.Context(), f.item, f.file, f.artifacts, f.modes, f.root, cachefs.Domain{})
	if err != nil {
		if owner != nil {
			owner.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	return owner
}

// Native primitive scope only. Actual Cmd/projection/whole-source proof is
// separately required; directly invoking Arm here is not a phase admission.
func TestNativeModeLeaseIsShortPerArtifactAndConcurrentBeginWaits(t *testing.T) {
	f := modeNativeFixtureV1(t, false)
	one, two := f.owner(t), f.owner(t)
	lease, err := one.BeginExecMode(t.Context())
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	loan, err := lease.RetainForJailV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loan.Close()
	if second, err := lease.RetainForJailV1(t.Context()); err == nil || second != nil {
		if second != nil {
			second.Close()
		}
		t.Fatal("the original mode source admitted a second independent private Cmd")
	}

	global, err := cachefs.OpenFile(f.root, ".lock", os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	held, err := cachefs.TryLock(global, cachefs.Exclusive)
	global.Close()
	if err != nil || !held {
		t.Fatal("artifact lease retained the global store lock", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	if other, err := two.BeginExecMode(ctx); !errors.Is(err, context.DeadlineExceeded) || other != nil {
		if other != nil {
			other.Close()
		}
		t.Fatal("concurrent start escaped the original native lease", err)
	}
	cancel()
	if err := lease.ArmV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := f.file.Stat()
	if err != nil || info.Mode().Perm() != 0o111 {
		t.Fatal("same native inode did not become execute-only", err)
	}
	if err := lease.Close(); !errors.Is(err, ErrExecModePending) {
		t.Fatal("uncertain armed lease was released", err)
	}
	// This fixture has no future-exec participant. Close its only retained loan
	// before the explicit primitive restoration, exactly as the native proof
	// path does after final exec/death in the real caller.
	loan.Close()
	if err := lease.RestoreFinalV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err = f.file.Stat()
	if err != nil || info.Mode().Perm() != 0o555 {
		t.Fatal("mode restoration changed the owned inode", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	next, err := two.BeginExecMode(ctx)
	if err != nil || next == nil {
		t.Fatal("next start waited for VM lifetime instead of final boundary", err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}
