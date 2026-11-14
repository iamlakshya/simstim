//go:build linux

package guest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVMReservedTargetsRefuseImageSymlinkAncestors(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "opt")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := vmDirectory(root, "opt/tools/bin", 0o755); err == nil {
		t.Fatal("image symlink selected a reserved tools ancestor")
	}
	if _, err := os.Stat(filepath.Join(outside, "tools")); !os.IsNotExist(err) {
		t.Fatal("unsafe target changed its alias destination")
	}
	for _, name := range []string{"../escape", "/absolute", "a/../b"} {
		if vmDirectory(root, name, 0o755) == nil {
			t.Fatal("unsafe reserved target accepted")
		}
	}
}

func TestVMSetupFileReplacesFinalSymlinkWithoutWritingItsTarget(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "unchanged")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "etc/passwd")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := vmAtomicFile(root, "etc/passwd", []byte("fixed guest accounts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "outside" {
		t.Fatal("final image symlink was followed", err)
	}
	info, err := os.Lstat(filepath.Join(base, "etc/passwd"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("fixed account file was not atomically installed", err)
	}
}

func TestVMReservedAncestorOwnershipUsesPinnedDirectory(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "opt"), 0o777); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	called := false
	err = vmOwnedDirectory(root, "opt", 0o755, 0, 0, func(file *os.File, uid, gid int) error {
		current, err := root.Lstat("opt")
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(current, opened) || uid != 0 || gid != 0 {
			t.Fatal("untrusted ancestor selected another inode or identity")
		}
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatal("reserved ancestor was not pinned and owned", err)
	}
	info, err := root.Stat("opt")
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatal("image-writable trusted ancestor remained writable", err)
	}
}
