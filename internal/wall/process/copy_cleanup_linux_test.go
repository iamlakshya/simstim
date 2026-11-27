//go:build linux

package process

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyCleanupKeepsForeignReplacementAndOtherNames(t *testing.T) {
	directory := t.TempDir()
	parent, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	original, err := os.CreateTemp(directory, "owned-")
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	identity, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(original.Name())
	if err := os.Rename(original.Name(), filepath.Join(directory, "still-owned")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyUnlinkOwned(parent, name, identity, false); err == nil {
		t.Fatal("cleanup accepted a foreign replacement")
	}
	if data, err := os.ReadFile(filepath.Join(directory, name)); err != nil || string(data) != "foreign" {
		t.Fatal("cleanup removed foreign data")
	}
	if err := copyUnlinkOwned(parent, "still-owned", identity, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "still-owned")); !os.IsNotExist(err) {
		t.Fatal("exact owned file remains")
	}
	if data, err := os.ReadFile(filepath.Join(directory, name)); err != nil || string(data) != "foreign" {
		t.Fatal("exact cleanup swept another name")
	}
}

func TestMetadataProcessCannotIssueOriginalCopyOwner(t *testing.T) {
	for _, runtime := range []*sandbox{{pidfd: -1}, {pidfd: 7, copy: &CopyOwnerV1{native: &copyOwnerNativeV1{}}}} {
		if owner, err := NativeCopyOwnerV1(runtime, nil); err == nil || owner != nil {
			t.Fatal("metadata-only process issued an original copy owner")
		}
	}
}
