package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegularWriteReusesNativeNoFollowReader(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(directory, "owned"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := OpenRegularWrite(root, "owned")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("other"), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenRegularWrite(root, "missing"); err == nil {
		file.Close()
		t.Fatal("mutable reopen created a missing file")
	}
	if err := os.Symlink("owned", filepath.Join(directory, "link")); err != nil {
		t.Skipf("native symlink fixture unavailable: %v", err)
	}
	if file, err := OpenRegularWrite(root, "link"); err == nil {
		file.Close()
		t.Fatal("mutable reopen followed a link")
	}
	data, err := root.ReadFile("owned")
	if err != nil || string(data) != "other" {
		t.Fatalf("unexpected owned data: %q %v", data, err)
	}
}
