package client

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCopyPlanKeepsNestedEmptyLeavesWithoutChargingParents(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"tree/first/nested", "tree/second"} {
		if err := os.MkdirAll(filepath.Join(directory, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := buildCopyPlan(t.Context(), []CopySource{{Host: filepath.Join(directory, "tree"), Guest: "/opt/skills"}}, directory, api.DefaultCopyMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.files) != 2 || plan.bytes != 0 {
		t.Fatalf("empty leaves = %d, %v", len(plan.files), err)
	}
	for _, file := range plan.files {
		if !file.file.Directory || file.file.SHA256 != api.EmptyInputHash {
			t.Fatal("directory became an empty regular file")
		}
		if err = file.write(t.Context(), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(directory, "tree/second/new"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.files {
		if file.file.Path == "/opt/skills/second" {
			if err = file.write(t.Context(), io.Discard); err == nil {
				t.Fatal("source directory name set grew after sealing")
			}
		}
	}
}
