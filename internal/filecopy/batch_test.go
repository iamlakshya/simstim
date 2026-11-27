package filecopy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func batchRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	name, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, name
}

func TestCopyBatchReopensStagesWithoutOneDescriptorPerEntry(t *testing.T) {
	root, _ := batchRoot(t)
	var targets []Target
	for index := range 300 {
		targets = append(targets, Target{Path: fmt.Sprintf("file-%03d", index), Size: 1})
	}
	batch, err := NewBatch(root, targets, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if err := batch.Stage(t.Context(), target, bytes.NewReader([]byte("x"))); err != nil {
			batch.Close()
			t.Fatal(err)
		}
	}
	if err := batch.Commit(t.Context()); err != nil {
		batch.Close()
		t.Fatal(err)
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCopyBatchPreflightsAllDestinationsBeforeAnyWrite(t *testing.T) {
	root, _ := batchRoot(t)
	file, err := root.OpenFile("existing", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if batch, err := NewBatch(root, []Target{{Path: "new/tree/file", Size: 1}, {Path: "existing", Size: 1}}, false); err == nil {
		batch.Close()
		t.Fatal("existing target admitted")
	}
	if _, err := root.Lstat("new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight created a folder before rejecting another destination")
	}
}

func TestCopyBatchNoReplacePreservesFilePlantedAfterPreflight(t *testing.T) {
	root, _ := batchRoot(t)
	target := Target{Path: "notes", Size: 3}
	batch, err := NewBatch(root, []Target{target}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Stage(t.Context(), target, bytes.NewReader([]byte("new"))); err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("notes", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("planted"))
	file.Close()
	if err := batch.Commit(t.Context()); err == nil {
		t.Fatal("no-replace overwrote a late destination")
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := root.ReadFile("notes")
	if err != nil || string(data) != "planted" {
		t.Fatalf("late destination changed: %q %v", data, err)
	}
	file, err = root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := file.ReadDir(-1)
	file.Close()
	if err != nil || len(entries) != 1 {
		t.Fatalf("owned staging not reclaimed: %v %v", entries, err)
	}
}

func TestCopyBatchRefusesCaseAndAncestorCollisions(t *testing.T) {
	root, _ := batchRoot(t)
	for _, targets := range [][]Target{{{Path: "A", Size: 1}, {Path: "a", Size: 1}}, {{Path: "a", Size: 1}, {Path: "a/b", Size: 1}}, {{Path: "../outside", Size: 1}}, {{Path: ".git/config", Size: 1}}} {
		if batch, err := NewBatch(root, targets, true); err == nil {
			batch.Close()
			t.Fatalf("unsafe plan accepted: %+v", targets)
		}
	}
}

func TestCopyBatchForceReplacesLeafLinkWithoutChangingItsTarget(t *testing.T) {
	root, directory := batchRoot(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "notes")); err != nil {
		t.Skipf("native symlink fixture unavailable: %v", err)
	}
	target := Target{Path: "notes", Size: 3}
	batch, err := NewBatch(root, []Target{target}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	if err := batch.Stage(t.Context(), target, bytes.NewReader([]byte("new"))); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "original" {
		t.Fatal("force followed the planted final link")
	}
	info, err := root.Lstat("notes")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 {
		t.Fatalf("published file type/mode: %v %v", info, err)
	}
}

func TestCopyBatchRefusesParentLinkIntoGitMetadata(t *testing.T) {
	root, directory := batchRoot(t)
	if err := root.Mkdir(".git", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(directory, "d")); err != nil {
		t.Skipf("native symlink fixture unavailable: %v", err)
	}
	if batch, err := NewBatch(root, []Target{{Path: "d/hooks", Size: 1}}, true); err == nil {
		batch.Close()
		t.Fatal("parent link into Git metadata admitted")
	}
	if _, err := root.Lstat(".git/hooks"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planted Git target changed")
	}
}
