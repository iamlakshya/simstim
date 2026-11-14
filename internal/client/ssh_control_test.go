//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The full temporary bind budget includes the epoch, nf prefix, route
// hash and OpenSSH suffix. A test owns both candidate locations.
func TestControlDirFitsASocketPath(t *testing.T) {
	sshDirs(t)
	short, err := os.MkdirTemp(testhome.Root(), "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	t.Setenv("XDG_CACHE_HOME", short)
	want := filepath.Join(short, brand.Name, "cm")
	if len(want)+1+26+1+3+40+17 > 103 {
		want = controlFallbackDir(uint32(os.Getuid()))
	}
	if got := controlDir(); got != want {
		t.Errorf("short cache: %q, want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), strings.Repeat("x", 80)))
	d := controlDir()
	if want := controlFallbackDir(uint32(os.Getuid())); d != want {
		t.Errorf("long cache: %q, want owned fallback %q", d, want)
	}
	if !controlFits(d) || !privateDir(d) {
		t.Errorf("unsafe or over-budget control base %q", d)
	}
}

// The historical runtime test now covers the actual H3 fallback base.
// Unsafe permissions, links and foreign ownership refuse sharing.
func TestControlDirWantsASafeRuntimeFolder(t *testing.T) {
	sshDirs(t)
	base := controlFallbackDir(uint32(os.Getuid()))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), strings.Repeat("x", 80)))
	other := t.TempDir()
	for _, c := range []struct {
		name    string
		make    func() error
		foreign bool
		safe    bool
	}{
		{"private", func() error { return os.Mkdir(base, 0700) }, false, true},
		{"world-writable", func() error { return os.Mkdir(base, 0777) }, false, false},
		{"group-writable", func() error { return os.Mkdir(base, 0770) }, false, false},
		{"world-writable without runtime", func() error { t.Setenv("XDG_RUNTIME_DIR", ""); return os.Mkdir(base, 0777) }, false, false},
		{"a link", func() error { return os.Symlink(other, base) }, false, false},
		{"another user's", func() error { return os.Mkdir(base, 0700) }, true, false},
	} {
		_ = os.RemoveAll(base)
		if err := c.make(); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(c.name, "world-writable") {
			if err := os.Chmod(base, 0777); err != nil {
				t.Fatal(err)
			}
		}
		if c.name == "group-writable" {
			if err := os.Chmod(base, 0770); err != nil {
				t.Fatal(err)
			}
		}
		owner := ownerOf
		if c.foreign {
			ownerOf = func(info os.FileInfo) int {
				if info.Name() == filepath.Base(base) {
					return os.Getuid() + 1
				}
				return owner(info)
			}
		}
		got := controlDir()
		ownerOf = owner
		want := ""
		if c.safe {
			want = base
		}
		if got != want {
			t.Errorf("%s base: %q, want %q", c.name, got, want)
		}
		if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
			t.Errorf("followed a link into another directory: %v %v", entries, err)
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
