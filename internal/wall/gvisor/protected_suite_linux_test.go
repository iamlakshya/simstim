//go:build linux

package gvisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// Metadata and native-object fixtures only. No runtime code is executed.
func protectedSuiteFixtureV1(t *testing.T) (supply.Manifest, *Runtime) {
	t.Helper()
	manifest := runtimeManifest(t)
	items, err := RuntimeItemsV1(manifest, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	owner := &Runtime{}
	t.Cleanup(func() { _ = owner.Close() })
	folder := t.TempDir()
	for _, item := range items {
		path := filepath.Join(folder, item.Name)
		if err := os.WriteFile(path, []byte("fixture "+item.Name), 0o555); err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		sealed, sealErr := supply.SealRuntime(context.Background(), item, source)
		closeErr := source.Close()
		if sealErr != nil || closeErr != nil {
			t.Fatal(errors.Join(sealErr, closeErr))
		}
		owner.executables = append(owner.executables, Executable{Target: RuntimeTargetV1(item.Name), File: sealed})
		if item.Name == "runsc" {
			owner.program, owner.item = sealed, item
		} else {
			owner.files = append(owner.files, sealed)
		}
	}
	owner.protectedSuite, err = captureProtectedSuiteV1(context.Background(), owner, items)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, owner
}

func TestProtectedSuiteDerivesReadonlySameSealedObjects(t *testing.T) {
	manifest, owner := protectedSuiteFixtureV1(t)
	suite := owner.protectedSuite
	if err := suite.CheckOriginalV1(context.Background(), manifest, owner.executables); err != nil {
		t.Fatal(err)
	}
	for index, held := range suite.files {
		flags, err := unix.FcntlInt(held.Fd(), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
			t.Fatal("sealed code did not acquire its required read-only description", flags, err)
		}
		expected, _ := owner.executables[index].File.Stat()
		actual, _ := held.Stat()
		if !os.SameFile(expected, actual) {
			t.Fatal("read-only code changed its original sealed inode")
		}
		if _, err := held.WriteAt([]byte("x"), 0); !errors.Is(err, unix.EBADF) {
			t.Fatal("read-only code retained a write description", err)
		}
	}
	code, err := suite.RetainControllerCodeV1(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer code.Close()
	for _, file := range []*os.File{code.Runsc, code.ProcUmounter, code.Sentry, code.FDParking, code.Prewarmer} {
		if file == nil {
			t.Fatal("the complete fixed controller projection lost a signed companion")
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := suite.CheckOriginalV1(context.Background(), manifest, nil); err == nil {
		t.Fatal("a closed original suite became a retained runtime owner")
	}
}

func TestProtectedControllerCannotAdoptCopiedMissingOrSubstitutedSuite(t *testing.T) {
	manifest, owner := protectedSuiteFixtureV1(t)
	for _, suite := range []*ProtectedSuiteV1{nil, {}, {runtime: owner}} {
		if err := suite.CheckOriginalV1(context.Background(), manifest, owner.executables); err == nil {
			t.Fatal("data manufactured protected code custody")
		}
	}
	copy := *owner.protectedSuite
	if err := copy.CheckOriginalV1(context.Background(), manifest, owner.executables); err == nil {
		t.Fatal("a copied suite adopted the original runtime")
	}
	changed := append([]Executable(nil), owner.executables...)
	changed[0].File = changed[1].File
	if err := owner.protectedSuite.CheckOriginalV1(context.Background(), manifest, changed); err == nil {
		t.Fatal("another signed role replaced the original runsc object")
	}
	other := manifest
	other.Items = append([]supply.Item(nil), manifest.Items...)
	other.Items[0].SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := owner.protectedSuite.CheckOriginalV1(context.Background(), other, nil); err == nil {
		t.Fatal("the protected code suite accepted another original signed manifest")
	}
	missing := manifest
	missing.Items = append([]supply.Item(nil), manifest.Items[:len(manifest.Items)-1]...)
	if _, err := owner.protectedSuite.RetainControllerCodeV1(context.Background(), missing); err == nil {
		t.Fatal("historical incomplete companions became a complete fixed controller suite")
	}
}
