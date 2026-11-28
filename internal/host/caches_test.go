package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/sandbox"
)

func TestNewSandboxedWalledCacheResolution(t *testing.T) {
	origProbe := probeProcessWall
	t.Cleanup(func() { probeProcessWall = origProbe })
	probeProcessWall = func(context.Context) (bool, string, string, string) {
		return true, "0.12.0", "", ""
	}

	t.Run("refuses .cargo/bin with cache_path_invalid", func(t *testing.T) {
		_, err := newProcessWalled(NewArgs{
			Wall:   "process",
			Agent:  "shell",
			Caches: []string{".cargo/bin"},
		})
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "cache_path_invalid" || pe.Hint == "" {
			t.Fatalf("newProcessWalled(.cargo/bin) = %#v, want proto.Error{Code:cache_path_invalid}", err)
		}
	})

	t.Run("resolve defaults, drop and off", func(t *testing.T) {
		def, err := sandbox.ResolveCaches(true)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(def, ".npm/_cacache") || !slices.Contains(def, ".local/share/pnpm/store") {
			t.Fatalf("default caches missing npm/pnpm: %v", def)
		}
		dropped, err := sandbox.ResolveCaches(true, []string{"!.npm/_cacache", ".cache/extra"})
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(dropped, ".npm/_cacache") || !slices.Contains(dropped, ".cache/extra") {
			t.Fatalf("dropped caches = %v", dropped)
		}
		off, err := sandbox.ResolveCaches(false)
		if err != nil || len(off) != 0 {
			t.Fatalf("off caches = (%v, %v), want empty", off, err)
		}
	})
}

func TestRemoveSessionDirReadOnlyCacheAndSymlinkSafety(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0o700) })

	sessDir := filepath.Join(tmp, "session-ab234567")
	roModDir := filepath.Join(sessDir, "caches", "go", "pkg", "mod", "example.com", "mod@v1.0.0")
	if err := os.MkdirAll(roModDir, 0o700); err != nil {
		t.Fatal(err)
	}
	roFile := filepath.Join(roModDir, "go.mod")
	if err := os.WriteFile(roFile, []byte("module example.com/mod\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sessDir, "caches", "escape-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roModDir, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(roModDir), 0o555); err != nil {
		t.Fatal(err)
	}

	if err := removeSessionDir(sessDir); err != nil {
		t.Fatalf("removeSessionDir: %v", err)
	}
	if _, err := os.Stat(sessDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session dir still exists after removeSessionDir: err=%v", err)
	}
	fi, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("outside dir missing: %v", err)
	}
	if fi.Mode().Perm() != 0o555 {
		t.Fatalf("outside dir permissions changed to %o, want 0555 (symlink must never be followed)", fi.Mode().Perm())
	}
}
