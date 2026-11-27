//go:build linux

package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestActualProcessPreparedReleaseIsNonDestructiveAndFencesStart(t *testing.T) {
	dir := t.TempDir()
	spec := wall.Spec{SessionDir: dir, Helper: t.TempDir(), Root: wall.Reset}
	resource, err := (Driver{}).Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	actual := resource.(*sandbox)
	if err := actual.ReleasePreparedV1(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "process.json")); err != nil {
		t.Fatal("prepared release removed durable metadata", err)
	}
	if _, err := actual.Start(t.Context()); err == nil {
		t.Fatal("released actual process factory restarted")
	}
}

func TestProcessPreparedNativeCloseErrorIsRetained(t *testing.T) {
	resource, err := newSandbox(wall.Spec{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	actual := resource.(*sandbox)
	if err := actual.storage.root.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := actual.ReleasePreparedV1(); err == nil {
			t.Fatal("lost actual metadata close error became success")
		}
	}
}
