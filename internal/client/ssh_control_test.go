//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestControlDirFitsASocketPath(t *testing.T) {
	short := t.TempDir()
	if len(short) > 50 {
		short = "/tmp"
	}
	t.Setenv("XDG_CACHE_HOME", short)
	if d := controlDir(); d != filepath.Join(short, brand.Name, "cm") && !strings.HasPrefix(d, "/tmp/") {
		t.Errorf("short cache: %q", d)
	}

	long := filepath.Join(t.TempDir(), strings.Repeat("x", 80))
	t.Setenv("XDG_CACHE_HOME", long)
	d := controlDir()
	if d == "" {
		t.Fatal("no control folder for a long cache path")
	}
	if len(d)+1+len(controlPrefix)+40 > maxSocketPath {
		t.Errorf("%q is too long for a socket path", d)
	}
	if !privateDir(d) {
		t.Errorf("%q is not private", d)
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
