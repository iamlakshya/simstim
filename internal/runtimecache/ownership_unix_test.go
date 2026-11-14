//go:build unix

package runtimecache

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStoreRefusesForeignOwnedDestinationWithoutChangingIt(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root in the isolated ownership job")
	}
	store, dir := testStore(t)
	body := []byte("data")
	item := testManifest(body, "r1").Items[0]
	target := filepath.Join(dir, "sha256", item.SHA256)
	if err := os.WriteFile(target, body, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	p, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Finish(t.Context()); err == nil {
		t.Fatal("foreign-owned file accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("foreign file changed: %q %v", got, err)
	}
	st, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != 65534 {
		t.Fatalf("foreign owner changed: %v", err)
	}
	assertNoParts(t, dir)
}

func TestStoreDoesNotChmodUnsafeExistingDirectory(t *testing.T) {
	root := cacheTestRoot(t)
	path := filepath.Join(root, "cache")
	if err := os.Mkdir(path, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("unsafe cache directory accepted")
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o777 {
		t.Fatalf("existing directory chmodded: %v %v", st, err)
	}
}

func TestStoreConstructorsRefuseForeignOwnedSentinel(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root in the isolated ownership job")
	}
	store, dir := testStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".lock")
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(string) (*Store, error){Open, OpenReadOnly} {
		if accepted, err := open(dir); err == nil {
			accepted.Close()
			t.Fatal("foreign-owned sentinel accepted")
		}
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	sys, ok := after.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != 65534 || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatalf("foreign-owned sentinel changed: %v", err)
	}
}
