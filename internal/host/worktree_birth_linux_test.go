//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestWorktreeNativeGenerationRejectsReplacementV2(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "work")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		t.Fatal(err)
	}
	birth, err := captureWorktreeBirthV2(root)
	if err != nil {
		t.Fatal(err)
	}
	if birth == nil {
		_ = root.Close()
		t.Skip("this filesystem has no supported native generation handle; no positive acceptance")
	}
	if err := checkWorktreeBirthV2(root, birth); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := checkWorktreeBirthV2(replacement, birth); err == nil {
		t.Fatal("a replacement directory satisfied the original kernel generation")
	}
	if err := checkWorktreeBirthV2(replacement, nil); err == nil {
		t.Fatal("a missing legacy birth acquired cleanup authority")
	}
}

func TestWorktreePartialUnlinkKeepsOriginalNativeRootV3(t *testing.T) {
	parent, err := cachefs.OpenPrivateRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := parent.Mkdir("work", 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := parent.OpenRoot("work")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ref, err := nativeWorktreeDirectoryRefV1(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := &ordinaryWorktreeCleanupV1{root: root, parent: parent, origin: worktreeOriginV1{Directory: ref}}
	file, err := root.OpenFile("partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cachefs.RenameOwnedDirectoryNoReplace(parent, "work", "capture"); err != nil {
		t.Fatal(err)
	}
	if err := parent.Mkdir("work", 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := parent.Lstat("work")
	if err != nil {
		t.Fatal(err)
	}
	if unlinked, err := owner.originalDirectoryUnlinkedV3(); err != nil || unlinked {
		t.Fatalf("partial cleanup lost the original native root: %v", err)
	}
	if err := root.Remove("partial"); err != nil {
		t.Fatal(err)
	}
	if err := parent.Remove("capture"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if unlinked, err := owner.originalDirectoryUnlinkedV3(); err != nil || !unlinked {
			t.Fatalf("retry did not observe SAME original native unlink: %v", err)
		}
	}
	current, err := parent.Lstat("work")
	if err != nil || !os.SameFile(current, replacement) {
		t.Fatal("retry adopted or removed the replacement", err)
	}
}
