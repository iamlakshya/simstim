//go:build linux

package gvisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func freshHomeStorageFixtureV1(t *testing.T) *runtimeStorage {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		name := filepath.Join(dir, key)
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, name)
	}
	session, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateChild(session, "gvisor", true)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := cachefs.OpenFile(session, "gvisor.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	storage := &runtimeStorage{session: session, root: root, lease: lease}
	t.Cleanup(func() { _ = storage.release() })
	return storage
}

func TestGvisorFreshHomeHasExclusivePrivateCreationAndIndependentReset(t *testing.T) {
	storage := freshHomeStorageFixtureV1(t)
	first, err := createFreshForkHomeV1(storage)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	second, err := createFreshForkHomeV1(storage)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	if first.name == second.name {
		t.Fatal("a new prepared runtime adopted the previous run home")
	}
	wal, err := first.root.OpenFile("state.sqlite-wal", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := wal.WriteString("live WAL")
	if err := errors.Join(writeErr, wal.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := second.root.Lstat("state.sqlite-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the fresh home retained another run's file", err)
	}
	info, err := first.file.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("the actual new home is not its private directory", info, err)
	}
}

func TestGvisorFreshHomePreservesSameNameReplacement(t *testing.T) {
	storage := freshHomeStorageFixtureV1(t)
	home, err := createFreshForkHomeV1(storage)
	if err != nil {
		t.Fatal(err)
	}
	defer home.file.Close()
	defer home.root.Close()
	if err := storage.root.Rename(home.name, "held-original-home"); err != nil {
		t.Fatal(err)
	}
	if err := storage.root.Mkdir(home.name, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := home.close(); err == nil {
		t.Fatal("an abandoned/replaced home became this creator's deletion authority")
	}
	if _, err := storage.root.Lstat(home.name); err != nil {
		t.Fatal("cleanup removed a same-name replacement", err)
	}
}

func TestGvisorHomeAccountingCannotBeMintedFromCopiedSandboxState(t *testing.T) {
	for _, sandbox := range []*sandbox{nil, {}, {storage: &runtimeStorage{}}} {
		called := false
		if err := sandbox.WithPreparedForkHomeV1(context.Background(), func(*os.Root) error { called = true; return nil }); err == nil || called {
			t.Fatal("copied runtime data supplied a prepared home disk capability")
		}
	}
}
