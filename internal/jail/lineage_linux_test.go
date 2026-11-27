package jail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLineageRejectsAnotherInodeAndSymlinkAtTheBase(t *testing.T) {
	root := hiddenPath(t.TempDir())
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	for _, path := range []string{left, right} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := openPath(unix.AT_FDCWD, left, unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if _, err := lineage(fd, left); err != nil {
		t.Fatalf("lineage of the unchanged bound inode: %v", err)
	}
	if _, err := lineage(fd, right); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Errorf("lineage accepted another inode: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(left, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := lineage(fd, alias); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Errorf("lineage followed a symlink at the base: %v", err)
	}
}

func TestAFileOnlyJailStillRequiresOpenat2BeforeProbing(t *testing.T) {
	file, err := os.Open("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	oldOpen, oldProbe := openat2, usernsWorks
	defer func() { openat2, usernsWorks = oldOpen, oldProbe }()
	openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
	probed := false
	usernsWorks = func() (bool, error) { probed = true; return true, nil }
	c, err := Command(context.Background(), Spec{Argv: []string{"/proc/self/fd/3"}, Files: []*os.File{file}, Seccomp: ProfileConverter})
	if c != nil || !errors.Is(err, errNoOpenat2) || hintOf(err) == "" || probed {
		t.Errorf("file-only jail without openat2: command=%v error=%v probed=%v", c, err, probed)
	}
}

func TestLineageRejectsASymlinkInTheParent(t *testing.T) {
	root := hiddenPath(t.TempDir())
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(filepath.Join(parent, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	fd, err := openPath(unix.AT_FDCWD, parent+"/child", unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := lineage(fd, alias+"/child"); err == nil {
		t.Error("lineage followed a symlink in its parent")
	}
}

func TestLineageDoesNotTreatTheChildAsTheRoot(t *testing.T) {
	root := hiddenPath(t.TempDir())
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	fd, err := openPath(unix.AT_FDCWD, child, unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	// A self-bound directory has the same inode as its textual parent.
	// The dot alias models that equality without requiring a mount.
	line, err := lineage(fd, child+"/.")
	if err != nil {
		t.Fatal(err)
	}
	var parent unix.Stat_t
	if err := unix.Stat(root, &parent); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ancestor := range line[1:] {
		if ancestor == (devIno{parent.Dev, parent.Ino}) {
			found = true
		}
	}
	if !found {
		t.Errorf("lineage stopped at a repeated child inode: %v", line)
	}
}
