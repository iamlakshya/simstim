//go:build linux

package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestCapturedProcessPlanRetainsSourceAfterNameReplacement(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := files.ResolveGrant(wall.Mount{Host: source, Guest: "/work", Write: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, owned, err := CaptureMountPlanV1(context.Background(), []wall.Mount{resolved})
	if err != nil {
		t.Fatal(err)
	}
	defer owned[0].Close()
	want, err := owned[0].Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, source+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := owned[0].Stat()
	if err != nil || !os.SameFile(want, got) || plan[0].Identity != resolved.Identity {
		t.Fatalf("the captured grant changed with its old path: %v", err)
	}
	if _, retained, err := CaptureMountPlanV1(context.Background(), []wall.Mount{resolved}); err == nil || len(retained) != 0 {
		t.Fatal("a replacement path renewed the original grant")
	}
}
