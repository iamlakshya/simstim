//go:build linux

package gvisor

import (
	"context"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestStoragePreparationReturnsItsActualLeaseOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	storage, err := prepareStorage(ctx, wall.Spec{ID: "aaaa2222", SessionDir: t.TempDir()}, false)
	if err == nil || storage == nil || storage.lease == nil || storage.session == nil {
		t.Fatal("actual partial storage disappeared", storage, err)
	}
	if err := storage.release(); err != nil {
		t.Fatal(err)
	}
}

func TestGvisorNativeCloseErrorsStayWithActualOwners(t *testing.T) {
	storage, err := prepareStorage(t.Context(), wall.Spec{ID: "aaaa2222", SessionDir: t.TempDir()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.root.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := storage.release(); err == nil {
			t.Fatal("storage close error became cleanup success")
		}
	}
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{program: file}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runtime.Close(); err == nil {
			t.Fatal("runtime descriptor close error became cleanup success")
		}
	}
}

func TestFailedPublicCodeProjectionRemainsReachableFromRuntime(t *testing.T) {
	manifest, owner := protectedSuiteFixtureV1(t)
	code, err := owner.protectedSuite.RetainControllerCodeV1(t.Context(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := code.Runsc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := code.Close(); err == nil {
		t.Fatal("failed projection close was not reported")
	}
	for range 2 {
		if err := owner.Close(); err == nil {
			t.Fatal("parent lost its actual failed code projection")
		}
	}
}
