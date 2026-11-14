//go:build !windows

package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "paths-agent-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAgentSocketValidatesSessionID(t *testing.T) {
	xdg := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	id, err := api.RandomSessionID()
	if err != nil {
		t.Fatal(err)
	}
	got := AgentSocket(id)
	want := filepath.Join(xdg, brand.Name, "agent-"+id+".sock")
	if got != want {
		t.Fatalf("AgentSocket(%q) = %q, want %q", id, got, want)
	}

	for _, bad := range []string{
		"",
		"../escape",
		"a/b",
		"UPPERCASE",
		".hidden",
		"-dash",
		strings.Repeat("a", 65),
	} {
		if got := AgentSocket(bad); got != "" {
			t.Errorf("AgentSocket(%q) = %q, want empty", bad, got)
		}
	}
}

func TestEnsurePrivateRuntimeDirCreates0700Dir(t *testing.T) {
	xdg := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	dir, err := EnsurePrivateRuntimeDir()
	if err != nil {
		t.Fatalf("EnsurePrivateRuntimeDir() error = %v", err)
	}
	want := filepath.Join(xdg, brand.Name)
	if dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700 dir", fi.Mode())
	}
}

func TestEnsurePrivateRuntimeDirRejectsUnsafeXDGAndRuntimeDirsWithoutChmod(t *testing.T) {
	t.Run("relative XDG_RUNTIME_DIR", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "relative/runtime")
		if _, err := EnsurePrivateRuntimeDir(); err == nil {
			t.Fatal("expected error for relative XDG_RUNTIME_DIR")
		}
	})

	t.Run("symlink XDG_RUNTIME_DIR", func(t *testing.T) {
		base := shortTempDir(t)
		realDir := filepath.Join(base, "real")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(base, "xdg-link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", linkDir)
		if _, err := EnsurePrivateRuntimeDir(); err == nil {
			t.Fatal("expected error for symlink XDG_RUNTIME_DIR")
		}
	})

	t.Run("group accessible XDG_RUNTIME_DIR is rejected without chmod", func(t *testing.T) {
		xdg := shortTempDir(t)
		if err := os.Chmod(xdg, 0o750); err != nil { //nolint:gosec // G302: intentionally testing rejection of group-accessible runtime base
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", xdg)
		if _, err := EnsurePrivateRuntimeDir(); err == nil {
			t.Fatal("expected error for 0750 XDG_RUNTIME_DIR")
		}
		fi, err := os.Lstat(xdg)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o750 {
			t.Fatalf("XDG_RUNTIME_DIR permissions mutated to %04o", fi.Mode().Perm())
		}
	})

	t.Run("symlink RuntimeDir is rejected", func(t *testing.T) {
		xdg := shortTempDir(t)
		other := shortTempDir(t)
		if err := os.Symlink(other, filepath.Join(xdg, brand.Name)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", xdg)
		if _, err := EnsurePrivateRuntimeDir(); err == nil {
			t.Fatal("expected error for symlink RuntimeDir")
		}
	})

	t.Run("world or group accessible RuntimeDir is rejected without chmod", func(t *testing.T) {
		xdg := shortTempDir(t)
		rt := filepath.Join(xdg, brand.Name)
		if err := os.Mkdir(rt, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(rt, 0o755); err != nil { //nolint:gosec // G302: intentionally testing rejection of non-private runtime dir
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", xdg)
		if _, err := EnsurePrivateRuntimeDir(); err == nil {
			t.Fatal("expected error for 0755 RuntimeDir")
		}
		fi, err := os.Lstat(rt)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("RuntimeDir permissions mutated to %04o", fi.Mode().Perm())
		}
	})

	t.Run("wrong owner check helper", func(t *testing.T) {
		xdg := shortTempDir(t)
		fi, err := os.Lstat(xdg)
		if err != nil {
			t.Fatal(err)
		}
		wrongUID := uint32(os.Getuid()) + 1
		if err := checkPrivateDirInfo(xdg, fi, wrongUID); err == nil {
			t.Fatal("expected error for mismatched owner uid")
		}
	})

	t.Run("overlong runtime path", func(t *testing.T) {
		base := shortTempDir(t)
		long := filepath.Join(base, strings.Repeat("d", 110))
		if err := os.MkdirAll(long, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", long)
		if _, err := EnsurePrivateRuntimeDir(); err == nil {
			t.Fatal("expected error for overlong runtime path")
		}
	})
}
