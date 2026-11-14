package client

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// compactControlFixtureDir follows the exclusive creation/retry practice of
// testhome's compact temporary folders. Four random bytes retain the standard
// temporary name's 32 random bits in six path-safe base64 characters.
func compactControlFixtureDir(t *testing.T) string {
	t.Helper()
	base := filepath.Clean(os.TempDir())
	// A paced runner registers a private /tmp/<3 characters> as TMPDIR.
	// An unguarded native run may have macOS's long default TMPDIR instead.
	if !filepath.IsAbs(base) || !controlFits(filepath.Join(base, "AAAAAA")) || !privateDir(base) {
		base = "/tmp"
	}
	parent, err := os.Stat(base)
	if err != nil || !parent.IsDir() {
		t.Fatalf("control fixture parent %q: %v", base, err)
	}
	if base == "/tmp" && parent.Mode()&os.ModeSticky == 0 {
		t.Fatal("native temporary fallback is not sticky")
	}
	if !controlFits(filepath.Join(base, "AAAAAA")) {
		t.Fatal("control fixture cannot retain the full temporary bind budget")
	}
	for range 10000 {
		var random [4]byte
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatal(err)
		}
		name := base64.RawURLEncoding.EncodeToString(random[:])
		dir := filepath.Join(base, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			t.Fatalf("make the private control fixture: %v", err)
		}
		t.Cleanup(func() {
			current, err := os.Stat(base)
			if err != nil || !os.SameFile(parent, current) {
				t.Error("control fixture parent changed; left its child alone")
				return
			}
			info, err := os.Lstat(dir)
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			if err != nil || !controlFileOwned(info) {
				t.Error("control fixture ownership changed; left it alone")
				return
			}
			// Tests deliberately replace their own child with unsafe modes or a
			// symlink. RemoveAll does not follow a final symlink into its target.
			if err := os.RemoveAll(dir); err != nil {
				t.Error(err)
			}
		})
		if !controlFits(dir) || !privateDir(dir) {
			t.Fatal("control fixture is unsafe or exceeds the full bind budget")
		}
		return dir
	}
	t.Fatal("no exclusive compact control fixture name remained after 10000 attempts")
	return ""
}

func TestControlFixtureDirectoriesAreExclusiveAndFit(t *testing.T) {
	if !controlMaster {
		t.Skip("this platform has no shared SSH control sockets")
	}
	var dirs []string
	var parent string
	var parentInfo os.FileInfo
	var untouched string
	t.Run("owned", func(t *testing.T) {
		dirs = []string{compactControlFixtureDir(t), compactControlFixtureDir(t)}
		if dirs[0] == dirs[1] {
			t.Fatal("two control fixtures shared a directory")
		}
		parent = filepath.Dir(dirs[0])
		var err error
		parentInfo, err = os.Stat(parent)
		if err != nil {
			t.Fatal(err)
		}
		untouched, err = os.MkdirTemp(parent, "untouched-")
		if err != nil {
			t.Fatal(err)
		}
		// This sibling remains after both control children run cleanup.
		for _, dir := range dirs {
			info, err := os.Stat(dir)
			if err != nil || !controlFits(dir) || info.Mode().Perm() != 0o700 || !controlFileOwned(info) {
				t.Fatalf("invalid owned control fixture %q: %v", dir, err)
			}
		}
	})
	defer os.RemoveAll(untouched)
	for _, dir := range dirs {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("control fixture was not cleaned: %s, %v", dir, err)
		}
	}
	current, err := os.Stat(parent)
	if err != nil || !os.SameFile(parentInfo, current) {
		t.Fatal("child cleanup replaced or removed its parent")
	}
	if _, err := os.Stat(untouched); err != nil {
		t.Fatal("child cleanup removed an unrelated sibling")
	}
}
