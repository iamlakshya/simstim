package jail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func targetRoot(t *testing.T, root string) (int, uint64) {
	t.Helper()
	fd, err := openPath(unix.AT_FDCWD, root, unix.RESOLVE_NO_SYMLINKS|unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	return fd, st.Dev
}

func TestMountTargetsPreserveOrdinarySymlinksInsideTheRoot(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{"var", "etc"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, link := range map[string]string{"var/lock": "../run/lock", "var/abs": "/opt/real",
		"etc/resolv.conf": "../run/systemd/resolve/stub-resolv.conf"} {
		if err := os.Symlink(link, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	fd, dev := targetRoot(t, root)
	for _, c := range []struct {
		path, resolved string
		dir            bool
	}{{"/var/lock/sub", "/run/lock/sub", true}, {"/var/abs/sub", "/opt/real/sub", true},
		{"/etc/resolv.conf", "/run/systemd/resolve/stub-resolv.conf", false}} {
		target, err := resolveTarget(fd, dev, c.path, c.dir, true)
		if err != nil {
			t.Fatalf("resolve %s: %v", c.path, err)
		}
		var actual, expected unix.Stat_t
		err = unix.Fstat(target.dstFD, &actual)
		target.close()
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Stat(root+c.resolved, &expected); err != nil {
			t.Fatal(err)
		}
		if actual.Dev != expected.Dev || actual.Ino != expected.Ino {
			t.Errorf("%s did not resolve to %s", c.path, c.resolved)
		}
	}
}

func TestMountTargetLinksNeverWriteOutsideTheRoot(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(map[bool]string{false: "relative", true: "absolute"}[absolute], func(t *testing.T) {
			root, outside := hiddenPath(t.TempDir()), hiddenPath(t.TempDir())
			if err := os.Chmod(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "work"), 0o700); err != nil {
				t.Fatal(err)
			}
			link := outside
			if !absolute {
				link = "../../../../../../../../" + strings.TrimPrefix(outside, "/")
			}
			if err := os.Symlink(link, filepath.Join(root, "work/escape")); err != nil {
				t.Fatal(err)
			}
			fd, dev := targetRoot(t, root)
			target, err := resolveTarget(fd, dev, "/work/escape/new-dir/new-file", false, true)
			if err != nil {
				t.Fatal(err)
			}
			target.close()
			if _, err := os.Stat(filepath.Join(outside, "new-dir")); !os.IsNotExist(err) {
				t.Errorf("mount target wrote outside the root: %v", err)
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("outside folder's mode changed: %v, %v", info, err)
			}
			if _, err := os.Stat(root + outside + "/new-dir/new-file"); err != nil {
				t.Errorf("rooted target was not created inside newroot: %v", err)
			}
		})
	}
}

func TestMountTargetDoesNotCreateInAHostBind(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Mkdir(filepath.Join(root, "hostbind"), 0o755); err != nil {
		t.Fatal(err)
	}
	fd, dev := targetRoot(t, root)
	if _, err := resolveTarget(fd, ^dev, "/hostbind/missing/sub", true, true); err == nil || hintOf(err) == "" {
		t.Errorf("missing path in a host bind was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "hostbind/missing")); !os.IsNotExist(err) {
		t.Errorf("target creation modified a host bind: %v", err)
	}
	if _, err := resolveTarget(fd, dev, "/missing", true, false); err == nil || hintOf(err) == "" {
		t.Errorf("create=false made a missing target: %v", err)
	}
}

func TestMountTargetFailuresCloseEveryDescriptor(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Symlink("b", root+"/a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", root+"/b"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root+"/dir", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/file", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fd, dev := targetRoot(t, root)
	before := openFDs(t)
	for _, c := range []struct {
		path string
		dir  bool
	}{{"/a", true}, {"/dir", false}, {"/file", true}, {"/file/sub", true}, {"/", true}, {"/dir/..", true}} {
		if target, err := resolveTarget(fd, dev, c.path, c.dir, true); err == nil {
			target.close()
			t.Errorf("invalid target %s was accepted", c.path)
		} else if hintOf(err) == "" {
			t.Errorf("invalid target %s had no hint: %v", c.path, err)
		}
	}
	if added := newFDs(before, openFDs(t)); len(added) != 0 {
		t.Errorf("failed target lookups leaked descriptors: %v", added)
	}
}
