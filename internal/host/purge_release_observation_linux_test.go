//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPurgeCloseObservationKeepsOriginalObjectsV3(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("original", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := observeOriginalPurgeDescriptorCloseV3([]*os.File{file}, []*os.Root{root}); err == nil {
		t.Fatal("live original descriptors were acknowledged as closed")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "original")); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenFile(filepath.Join(path, "original"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	for range 2 {
		if err := observeOriginalPurgeDescriptorCloseV3([]*os.File{file}, []*os.Root{root}); err != nil {
			t.Fatal(err)
		}
		if _, err := replacement.Stat(); err != nil {
			t.Fatal("close retry touched a replacement descriptor", err)
		}
	}
	if err := observeOriginalPurgeDescriptorCloseV3([]*os.File{replacement}, nil); err == nil {
		t.Fatal("replacement descriptor supplied the original closure")
	}
}
