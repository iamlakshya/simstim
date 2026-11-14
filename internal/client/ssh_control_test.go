//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The sockets of shared connections go in the cache folder when their path
// fits there. Otherwise they go in the runtime folder, which the tests'
// home has (testhome): no test uses the shared folder in /tmp. The folder
// is private, and its socket paths fit.
func TestControlDirFitsASocketPath(t *testing.T) {
	short, err := os.MkdirTemp(testhome.Root(), "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", short)
	if d := controlDir(); d != filepath.Join(short, brand.Name, "cm") {
		t.Errorf("short cache: %q", d)
	}

	long := filepath.Join(t.TempDir(), strings.Repeat("x", 80))
	t.Setenv("XDG_CACHE_HOME", long)
	d := controlDir()
	if d != filepath.Join(paths.RuntimeDir(), "cm") || !strings.HasPrefix(d, testhome.Root()+"/") {
		t.Errorf("a long cache: %q, want cm in the runtime folder %s", d, paths.RuntimeDir())
	}
	if len(d)+1+len(controlPrefix)+40 > maxSocketPath {
		t.Errorf("%q is too long for a socket path", d)
	}
	if !privateDir(d) {
		t.Errorf("%q is not private", d)
	}
}

// The runtime folder holds the sockets only when it is safe: the user's,
// not a link, and not writable by its group or others. Otherwise the
// sockets go in the next place, never under a folder that someone else
// controls.
func TestControlDirWantsASafeRuntimeFolder(t *testing.T) {
	root, err := os.MkdirTemp(testhome.Root(), "r")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), strings.Repeat("x", 80)))
	last := filepath.Join(root, "last")
	old := tmpControlDir
	tmpControlDir = func() string { return last }
	defer func() { tmpControlDir = old }()
	runtime, cm := filepath.Join(root, brand.Name), filepath.Join(root, brand.Name, "cm")
	other := t.TempDir()
	for _, c := range []struct {
		name    string
		make    func() error
		foreign bool // the runtime folder seems another user's
		want    string
	}{
		{"private", func() error { return os.Mkdir(runtime, 0o700) }, false, cm},
		{"world-writable", func() error {
			if err := os.Mkdir(runtime, 0o700); err != nil {
				return err
			}
			return os.Chmod(runtime, 0o777)
		}, false, last},
		{"group-writable", func() error {
			if err := os.Mkdir(runtime, 0o700); err != nil {
				return err
			}
			return os.Chmod(runtime, 0o770)
		}, false, last},
		{"a link", func() error { return os.Symlink(other, runtime) }, false, last},
		{"world-writable, and no $XDG_RUNTIME_DIR", func() error {
			// The runtime folder is then <name>-<uid> in the temporary
			// folder, which another user may have made first.
			t.Setenv("XDG_RUNTIME_DIR", "")
			t.Setenv("TMPDIR", root)
			runtime, cm = paths.RuntimeDir(), filepath.Join(paths.RuntimeDir(), "cm")
			if err := os.Mkdir(runtime, 0o700); err != nil {
				return err
			}
			return os.Chmod(runtime, 0o777)
		}, false, last},
		{"another user's", func() error { return os.Mkdir(runtime, 0o700) }, true, last},
	} {
		_ = os.RemoveAll(runtime)
		_ = os.RemoveAll(last)
		if err := c.make(); err != nil {
			t.Fatal(err)
		}
		owner := ownerOf
		if c.foreign {
			name := filepath.Base(runtime)
			ownerOf = func(fi os.FileInfo) int {
				if fi.Name() == name {
					return os.Getuid() + 1
				}
				return owner(fi)
			}
		}
		got := controlDir()
		ownerOf = owner
		if got != c.want {
			t.Errorf("a runtime folder that is %s: %q, want %q", c.name, got, c.want)
		}
		if _, err := os.Lstat(filepath.Join(other, "cm")); err == nil {
			t.Errorf("a runtime folder that is %s: cm was made through the link", c.name)
		}
	}
}

func TestPrivateDir(t *testing.T) {
	d := t.TempDir()
	open := filepath.Join(d, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if privateDir(open) {
		t.Error("a folder others can read is private")
	}
	link := filepath.Join(d, "link")
	priv := filepath.Join(d, "priv")
	if err := os.Mkdir(priv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(priv, link); err != nil {
		t.Fatal(err)
	}
	if !privateDir(priv) || privateDir(link) {
		t.Errorf("private %v, link %v", privateDir(priv), privateDir(link))
	}
}
