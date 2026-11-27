package jail

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMountTargetsFinishDirectoryLinkSuffixes(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{"links", "run/lock", "run/up/sub", "opt/real"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fd, dev := targetRoot(t, root)
	for _, c := range []struct {
		name, link, want string
	}{
		{"relative-slash", "../run/lock/", "/run/lock"},
		{"absolute-slash", "/opt/real/", "/opt/real"},
		{"relative-dot", "../run/lock/.", "/run/lock"},
		{"relative-parent", "../run/up/sub/..", "/run/up"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := os.Symlink(c.link, filepath.Join(root, "links", c.name)); err != nil {
				t.Fatal(err)
			}
			target, err := resolveTarget(fd, dev, "/links/"+c.name, true, true)
			if err != nil {
				t.Fatal(err)
			}
			defer target.close()
			var got, want unix.Stat_t
			if err := unix.Fstat(target.dstFD, &got); err != nil {
				t.Fatal(err)
			}
			if err := unix.Stat(root+c.want, &want); err != nil {
				t.Fatal(err)
			}
			if got.Dev != want.Dev || got.Ino != want.Ino || target.path != c.want {
				t.Errorf("directory suffix resolved to %q inode%d:%d, want %q inode%d:%d",
					target.path, got.Dev, got.Ino, c.want, want.Dev, want.Ino)
			}
		})
	}
}

func TestFileMountRejectsDirectoryLinkSuffixBeforeCreation(t *testing.T) {
	for _, suffix := range []string{"/", "/.", "/sub/.."} {
		t.Run(suffix, func(t *testing.T) {
			root := hiddenPath(t.TempDir())
			if err := os.Symlink("/missing"+suffix, root+"/link"); err != nil {
				t.Fatal(err)
			}
			fd, dev := targetRoot(t, root)
			if target, err := resolveTarget(fd, dev, "/link", false, true); err == nil {
				target.close()
				t.Error("a file mount accepted a directory-only link expansion")
			} else if !strings.Contains(err.Error(), "not a directory") || hintOf(err) == "" {
				t.Errorf("directory-only file target error = %v, want ENOTDIR with a hint", err)
			}
			if _, err := os.Lstat(root + "/missing"); !os.IsNotExist(err) {
				t.Errorf("the rejected file target created an entry: %v", err)
			}
		})
	}
}

func TestTargetTraceSharesLinkAndParentSemanticsWithoutCreation(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Mkdir(root+"/real", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/real/.", root+"/alias"); err != nil {
		t.Fatal(err)
	}
	fd, dev := targetRoot(t, root)
	before := openFDs(t)
	for _, input := range []string{"/alias", "/missing/../alias", "/absent/deeper/../../alias"} {
		got, err := traceTarget(fd, dev, input, true)
		if err != nil || got != "/real" {
			t.Errorf("trace %q = %q, %v, want /real", input, got, err)
		}
	}
	for _, name := range []string{"missing", "absent"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("read-only planning created %s: %v", name, err)
		}
	}
	if after := openFDs(t); !maps.Equal(after, before) {
		t.Errorf("target tracing leaked descriptors: before%v after%v", before, after)
	}
}
