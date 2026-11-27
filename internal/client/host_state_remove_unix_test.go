//go:build !windows

package client

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHostStateRemovalPreservesForeignOwnerAndLinkedLock(t *testing.T) {
	for _, kind := range []string{"file-owner", "lock-owner", "linked-lock"} {
		t.Run(kind, func(t *testing.T) {
			original, ctx, _, folder, validators := hostStateRemovalFixture(t)
			name := "baseline.json"
			if kind != "file-owner" {
				name += ".lock"
			}
			path := filepath.Join(folder, name)
			prior := stateValidateDescriptor
			defer func() { stateValidateDescriptor = prior }()
			if kind == "linked-lock" {
				if err := os.Link(path, filepath.Join(t.TempDir(), "linked")); err != nil {
					t.Fatal(err)
				}
			} else {
				stateValidateDescriptor = func(f *os.File, mode os.FileMode) (os.FileInfo, error) {
					info, err := f.Stat()
					if err == nil && info.Name() == name {
						return nil, errors.New("canonical owner check refused")
					}
					return prior(f, mode)
				}
			}
			preserved, err := RemoveHostStateContext(ctx, original, strings.Repeat("a", 32), validators)
			if err == nil || !slices.Contains(preserved.Preserved, filepath.Join(folder, "baseline.json")) {
				t.Fatalf("foreign ownership not preserved: %+v,%v", preserved, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("foreign/linked file removed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(folder, "removal.json")); err != nil {
				t.Fatalf("journal removed after failed ownership: %v", err)
			}
		})
	}
}
