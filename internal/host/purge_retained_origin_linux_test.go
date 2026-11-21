//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These exercise real retained native objects only. No test can construct a
// positive ordinary capture, OP, purge origin or stopped Source from them.
func TestRetainedOriginRootRequiresActualOriginalUnlinkV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ref, err := nativeWorktreeDirectoryRefV1(root)
	if err != nil {
		t.Fatal(err)
	}
	retained := &purgeRetainedRootV2{root: root, path: path, ref: ref}
	if err := retained.checkV2(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := retained.checkV2(); err == nil {
		t.Fatal("missing name alone became original root closure")
	}
	if err := os.Remove(path + ".moved"); err != nil {
		t.Fatal(err)
	}
	if err := retained.checkV2(); err != nil {
		t.Fatal("same positively unlinked original object was not retained", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := retained.checkV2(); err == nil {
		t.Fatal("foreign replacement renewed the retained original root")
	}
}

func TestRetainedOriginFileDoesNotAdoptAReplacementV2(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := filepath.Join(path, "sentinel")
	if err := os.WriteFile(name, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	retained := &purgeRetainedFileV2{root: root, leaf: "sentinel", file: file, info: info}
	if err := retained.checkV2(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, name+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := retained.checkV2(); err == nil {
		t.Fatal("renamed live file became a positive unlink")
	}
	if err := os.Remove(name + ".moved"); err != nil {
		t.Fatal(err)
	}
	if err := retained.checkV2(); err != nil {
		t.Fatal("same native file was not retained after real unlink", err)
	}
	if err := os.WriteFile(name, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retained.checkV2(); err == nil {
		t.Fatal("replacement sentinel became the original native file")
	}
	raw, err := os.ReadFile(name)
	if err != nil || string(raw) != "unrelated" {
		t.Fatal("retained check changed replacement content", err)
	}
}

func TestRetainedPurgeOriginCannotOriginateFromMissingPathsV2(t *testing.T) {
	if origin, err := newPurgeRetainedOriginV2(context.Background(), nil); origin != nil || err == nil {
		t.Fatal("missing owner created retained native origin")
	}
	for _, origin := range []*purgeRetainedOriginV2{nil, {}, {owner: &purgeOwnerV1{}}} {
		if _, err := origin.contextV2(context.Background()); err == nil {
			t.Fatal("metadata created actual native purge context")
		}
	}
}
