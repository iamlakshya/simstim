//go:build linux

package supervisor

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
	sandboxpolicy "github.com/iamlakshya/simstim/internal/sandbox"
	"github.com/iamlakshya/simstim/internal/wall"
)

func ownedProcessDiskFixture(t *testing.T) (wall.Spec, *files.Capture) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	capture, err := files.BeginCapture(dir, files.StatePlan{Files: []string{".agent/state.sqlite"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	return wall.Spec{SessionDir: dir, Helper: t.TempDir(), DiskMax: 1 << 20, Mounts: []wall.Mount{{Host: work, Guest: "/work", Write: true}}, StateDirs: []string{".cache/tool"}, StateFiles: []string{".agent/state.sqlite"}, StateRestore: capture.Restore, StateCapture: capture.Incoming}, capture
}

func diskRootInfo(t *testing.T, root *os.Root) os.FileInfo {
	t.Helper()
	file, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestActualProcessFactoryDiskRootsChargePrivateSessionAndExcludeExternalSources(t *testing.T) {
	spec, capture := ownedProcessDiskFixture(t)
	foreign, cached := t.TempDir(), t.TempDir()
	spec.Mounts = append(spec.Mounts, wall.Mount{Host: foreign, Guest: "/mnt/external", Write: true}, wall.Mount{Host: cached, Guest: "/opt/cache"})
	resources, err := processDiskResourcesV1(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Close()
	options, err := resources.options(func(error) {})
	if err != nil || !options.process || options.limit != spec.DiskMax || len(options.roots) != 1 {
		t.Fatal("real factory did not issue the exact private writable roles", options, err)
	}
	wanted := []string{spec.SessionDir}
	for _, name := range wanted {
		want, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, root := range options.roots {
			found = found || os.SameFile(want, diskRootInfo(t, root))
		}
		if !found {
			t.Fatal("private writable role was not pinned", name)
		}
	}
	for _, name := range []string{foreign, cached, capture.Restore, capture.Incoming} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, root := range options.roots {
			if os.SameFile(info, diskRootInfo(t, root)) {
				t.Fatal("external sources or active-only state replaced complete private session accounting", name)
			}
		}
	}
	options.roots[0] = nil
	next, err := resources.options(func(error) {})
	if err != nil || next.roots[0] == nil {
		t.Fatal("observer replaced its owner's root vector", err)
	}
}

func TestCacheDropAndRotationCannotEvadePrivateSessionDiskCap(t *testing.T) {
	spec, oldCapture := ownedProcessDiskFixture(t)
	spec.DiskMax = 256 << 10
	first, err := processDiskResourcesV1(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Controlled incompressible cache data, never a sparse apparent-size test.
	body := make([]byte, 1<<20)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spec.SessionDir, "state/.cache/tool/package"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := oldCapture.Close(); err != nil {
		t.Fatal(err)
	}
	no, yes := false, true
	for _, selection := range []api.SandboxValues{{KeepCaches: &no, Caches: []string{".cache/tool"}}, {KeepCaches: &yes, Caches: []string{".cache/rotated"}}} {
		plan, err := sandboxpolicy.CacheStatePlan(files.StatePlan{Files: []string{".agent/state.sqlite"}}, &selection)
		if err != nil {
			t.Fatal(err)
		}
		capture, err := files.BeginCapture(spec.SessionDir, plan)
		if err != nil {
			t.Fatal(err)
		}
		next := spec
		next.StateDirs, next.StateFiles = plan.Dirs, plan.Files
		next.StateRestore, next.StateCapture = capture.Restore, capture.Incoming
		resources, err := processDiskResourcesV1(next)
		if err != nil {
			capture.Close()
			t.Fatal(err)
		}
		options, err := resources.options(func(error) {})
		if err != nil {
			resources.Close()
			capture.Close()
			t.Fatal(err)
		}
		options.space = func(*os.Root) (files.Space, error) { return files.Space{Available: 50, Total: 100}, nil }
		err = checkDisk(t.Context(), options) // Real canonical allocated-block walk.
		closeErr := errors.Join(resources.Close(), capture.Close())
		var failure *proto.Error
		if closeErr != nil || !errors.As(err, &failure) || failure.Code != "disk_full" {
			t.Fatal("a dropped or rotated cache bypassed the unchanged private session cap", selection, err, closeErr)
		}
	}
}

func TestFactoryDiskRootsRefuseAliasesAndMissingRoles(t *testing.T) {
	for _, alias := range []bool{false, true} {
		spec, _ := ownedProcessDiskFixture(t)
		if alias {
			if err := os.MkdirAll(filepath.Join(spec.SessionDir, "state/.cache"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(spec.SessionDir, "work"), filepath.Join(spec.SessionDir, "state/.cache/tool")); err != nil {
				t.Fatal(err)
			}
		} else {
			spec.Mounts = nil
		}
		if resources, err := processDiskResourcesV1(spec); err == nil {
			resources.Close()
			t.Fatal("a source alias or missing actual owned workspace minted storage authority", alias)
		}
	}
}

func TestDiskCapabilityRefusesZeroAndClosesOnlyAfterBoundedStartReturns(t *testing.T) {
	for _, empty := range []*diskResourcesV1{nil, {}} {
		if _, err := empty.options(func(error) {}); err == nil {
			t.Fatal("an unissued zero capability became runtime disk authority")
		}
	}
	spec, _ := ownedProcessDiskFixture(t)
	resources, err := processDiskResourcesV1(spec)
	if err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	defer resources.Close()
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	startDone := make(chan error, 1)
	go func() {
		_, err := resources.start(t.Context(), func(context.Context) (net.Conn, error) {
			close(entered)
			<-finish
			return nil, nil
		})
		startDone <- err
	}()
	<-entered
	closeAttempt, closeDone := make(chan struct{}), make(chan error, 1)
	go func() { close(closeAttempt); closeDone <- resources.Close() }()
	<-closeAttempt
	select {
	case err := <-closeDone:
		t.Fatal("owner closed resources during its still-owned Start", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := resources.options(func(error) {}); err == nil {
		t.Fatal("closed observer resources could be borrowed again")
	}
	if _, err := resources.start(t.Context(), func(context.Context) (net.Conn, error) {
		t.Fatal("discarded prepared runtime reached Start")
		return nil, nil
	}); err == nil {
		t.Fatal("closed resource capability allowed a later Start")
	}
	if err := resources.Close(); err != nil {
		t.Fatal("second owned close failed", err)
	}
}

func TestDiskCapabilityRetainsRootsUntilPreparedLeaseCanBeReleased(t *testing.T) {
	spec, _ := ownedProcessDiskFixture(t)
	resources, err := processDiskResourcesV1(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		resources.release = nil
		_ = resources.Close()
	})
	live := errors.New("the owned runtime is still live")
	resources.release = func() error { return live }
	if err := resources.Close(); !errors.Is(err, live) {
		t.Fatal("live lease refusal was lost", err)
	}
	if _, err := resources.options(func(error) {}); err != nil {
		t.Fatal("failed lease release prematurely closed borrowed roots", err)
	}
	resources.release = func() error { return nil }
	if err := resources.Close(); err != nil {
		t.Fatal(err)
	}
}
