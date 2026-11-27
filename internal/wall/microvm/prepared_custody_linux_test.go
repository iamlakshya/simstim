//go:build linux

package microvm

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestVMPreparationReturnsActualLeaseOwnerOnCancelledLock(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	driver := &Driver{kind: cloudHypervisorBackend}
	resource, err := driver.prepare(ctx, wall.Spec{SessionDir: t.TempDir()}, true, "login")
	if err == nil || resource == nil {
		t.Fatal("partial native factory owner was discarded", err)
	}
	actual := resource.(*sandbox)
	if actual.lease == nil || actual.preparedRoot == nil {
		t.Fatal("actual opened native roles were not adopted")
	}
	if _, err := actual.Start(t.Context()); err == nil {
		t.Fatal("failed preparation granted a later Start")
	}
	if err := actual.ReleasePreparedV1(); err != nil {
		t.Fatal(err)
	}
}

func TestVMPreparedCloseErrorStaysWithSameResource(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	resource, err := (&Driver{kind: cloudHypervisorBackend}).prepare(ctx, wall.Spec{SessionDir: t.TempDir()}, true, "login")
	if err == nil || resource == nil {
		t.Fatal(resource, err)
	}
	actual := resource.(*sandbox)
	if err := actual.preparedRoot.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := actual.ReleasePreparedV1(); err == nil {
			t.Fatal("failed original root close became a successful retry")
		}
	}
	if actual.preparedRoot == nil || actual.rootCloseErr == nil || !actual.preparedReleased {
		t.Fatal("the failed cleanup owner or Start fence was lost")
	}
}
