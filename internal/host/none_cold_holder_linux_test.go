//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// This owns only temporary test metadata and file locks. It exercises the
// no-create/substitution guard, and does not construct a continuation issuer.
func noneHolderMetadataFixtureV1(t *testing.T) (*SessionOperation, *Manifest) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	for key, value := range map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(base, "config"), "XDG_DATA_HOME": filepath.Join(base, "data"), "XDG_STATE_HOME": filepath.Join(base, "state"), "XDG_CACHE_HOME": filepath.Join(base, "cache"), "XDG_RUNTIME_DIR": filepath.Join(base, "run"), "XDG_CONFIG_DIRS": filepath.Join(base, "config-dirs"), "XDG_DATA_DIRS": filepath.Join(base, "data-dirs")} {
		t.Setenv(key, value)
		if err := os.MkdirAll(value, 0700); err != nil {
			t.Fatal(err)
		}
	}
	m := noneCleanFixtureV1()
	m.Run.BootID = BootID()
	m.NoneCleanup.Boot = m.Run.BootID
	if err := os.MkdirAll(paths.SessionDir(m.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	op, err := BeginSessionOperation(context.Background(), m.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(op.Close)
	file, err := cachefs.OpenFile(op.native.root, holderLockFile, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	m.NoneCleanup.Directory, err = cachefs.CaptureRoot(op.native.root)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	m.NoneCleanup.Holder, err = cachefs.CaptureFile(op.native.root, holderLockFile, file, 0600)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return op, &m
}

func TestNoneColdHolderRefusesMissingAndReplacedOriginalInodes(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "replaced"}[replacement], func(t *testing.T) {
			op, source := noneHolderMetadataFixtureV1(t)
			if err := op.native.root.Remove(holderLockFile); err != nil {
				t.Fatal(err)
			}
			if replacement {
				file, err := cachefs.OpenFile(op.native.root, holderLockFile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := openNoneColdHolderV1(context.Background(), op, source)
			if owner != nil {
				owner.Close()
				t.Fatal("a substituted original holder was adopted")
			}
			if err == nil {
				t.Fatal("a missing original holder was silently recreated")
			}
			if !replacement {
				if _, err := op.native.root.Lstat(holderLockFile); !os.IsNotExist(err) {
					t.Fatal("the no-create guard made a new holder inode", err)
				}
			}
		})
	}
}

func TestNoneColdHolderAcquiresOnlyOriginalNonblockingLease(t *testing.T) {
	op, source := noneHolderMetadataFixtureV1(t)
	owner, err := openNoneColdHolderV1(context.Background(), op, source)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	second, err := openNoneColdHolderV1(context.Background(), op, source)
	if second != nil {
		second.Close()
		t.Fatal("another description acquired the same original holder")
	}
	if err == nil {
		t.Fatal("the existing original holder lock was ignored")
	}
}

func TestCopiedNoneColdCannotReleaseOriginalNativeDescriptors(t *testing.T) {
	original := &NoneColdResumeV1{}
	original.self = original
	copy := new(NoneColdResumeV1)
	reflect.ValueOf(copy).Elem().Set(reflect.ValueOf(original).Elem())
	if err := copy.Close(); err == nil {
		t.Fatal("a shallow copy released an original None source")
	}
	if original.closed {
		t.Fatal("a copy changed the original source")
	}
	if err := copy.Check(context.Background(), &SessionOperation{}); err == nil {
		t.Fatal("a shallow copy issued a continuation")
	}
	if _, ok := any(original).(*StoppedSessionV1); ok {
		t.Fatal("a None source became whole-unit stopped authority")
	}
	if _, ok := any(original).(*StoppedOrdinarySessionV1); ok {
		t.Fatal("a None source became ordinary holder authority")
	}
	if _, ok := any(original).(*StoppedNoneSessionV1); ok {
		t.Fatal("a cold source became live N5 stop completion")
	}
}
