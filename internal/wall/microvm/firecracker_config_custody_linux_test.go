//go:build linux

package microvm

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestCancelledBootConfigReturnsSameActualResourcesForCleanupV3(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	spec := firecrackerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	owner, err := prepareFirecrackerConfig(ctx, spec)
	if !errors.Is(err, context.Canceled) || owner == nil || owner.root == nil || owner.file == nil || owner.temporary == "" {
		t.Fatal("cancelled construction dropped its actual partial resource owner", err)
	}
	original := owner.file
	files := &bootFiles{config: owner}
	if err := files.close(); err != nil {
		t.Fatal(err)
	}
	if files.config != nil || owner.root != nil || owner.file != nil || owner.temporary != "" {
		t.Fatal("same-owner cleanup did not finish its actual stack")
	}
	if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("cleanup did not retire its original actual descriptor", err)
	}
	entries, err := os.ReadDir(spec.SessionDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("actual cancelled temporary-file cleanup did not finish", entries, err)
	}
}

func TestBootConfigUnknownCloseRetainsSameStackAndDiagnosticV3(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	owner, err := prepareFirecrackerConfig(t.Context(), firecrackerFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	original, root := owner.file, owner.root
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	files := &bootFiles{config: owner}
	for range 2 {
		if err := files.close(); err == nil || !errors.Is(err, os.ErrClosed) {
			t.Fatal("unknown close became successful cleanup", err)
		}
		if files.config != owner || owner.file != original || owner.root != root || !errors.Is(files.configDiagnostic, os.ErrClosed) {
			t.Fatal("failed cleanup lost its actual owner or original diagnostic")
		}
	}
	// This negative never grants cleanup. Test process exit owns the already
	// closed descriptor; close only the unrelated actual retained test Root.
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
}
