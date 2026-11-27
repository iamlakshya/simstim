//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestGvisorFactoryRefusesUnissuedSpecBeforeMakingRuntimeStorage(t *testing.T) {
	driver, err := NewGvisorDriver()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spec := wall.Spec{ID: "session", SessionDir: dir, Helper: "/usr/bin", Root: wall.Reset}
	ctx := wall.WithExpectedSpec(context.Background(), spec)
	if _, err := driver.Prepare(ctx, spec); err == nil {
		t.Fatal("a caller's expected spec issued a native gvisor boot")
	}
	if _, err := os.Lstat(filepath.Join(dir, "gvisor")); !os.IsNotExist(err) {
		t.Fatal("an unauthenticated factory changed the session root", err)
	}
}

func TestGvisorTargetDeduplicationRequiresSameCapabilityAndRights(t *testing.T) {
	first, err := os.CreateTemp(t.TempDir(), "first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.CreateTemp(t.TempDir(), "second")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	bound := jail.Bind{Path: "/fixed/source", File: first}
	if got, err := coalesceGvisorBindsV1([]jail.Bind{bound, bound}); err != nil || len(got) != 1 {
		t.Fatal("identical capabilities did not coalesce", err)
	}
	for _, conflict := range []jail.Bind{{Path: bound.Path, File: second}, {Path: bound.Path, File: first, Write: true}, {Path: bound.Path}} {
		if _, err := coalesceGvisorBindsV1([]jail.Bind{bound, conflict}); err == nil {
			t.Fatal("an unrelated source or greater right replaced a captured target")
		}
	}
}
