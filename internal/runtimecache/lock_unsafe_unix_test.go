//go:build unix

package runtimecache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreConstructorsRefuseUnsafeSentinelWithoutChangingIt(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "wrong mode"} {
		t.Run(kind, func(t *testing.T) {
			store, dir := testStore(t)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(dir, ".lock")
			if err := os.Rename(sentinel, sentinel+".original"); err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(filepath.Dir(dir), "foreign-lock")
			if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(foreign, sentinel)
			case "hardlink":
				err = os.Link(foreign, sentinel)
			default:
				err = os.WriteFile(sentinel, []byte("unsafe"), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(sentinel)
			if err != nil {
				t.Fatal(err)
			}
			for _, open := range []func(string) (*Store, error){Open, OpenReadOnly} {
				if accepted, err := open(dir); err == nil {
					accepted.Close()
					t.Fatal("unsafe existing sentinel accepted")
				}
			}
			after, err := os.Lstat(sentinel)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("unsafe sentinel replaced, removed or chmodded: %v", err)
			}
			if data, err := os.ReadFile(foreign); err != nil || !bytes.Equal(data, []byte("keep")) {
				t.Fatalf("foreign sentinel target changed: %q %v", data, err)
			}
		})
	}
}
