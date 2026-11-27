package cachefs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func isolatedRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		path := filepath.Join(root, key)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	return root
}
func privateRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := filepath.Join(isolatedRoot(t), "cache")
	root, err := OpenPrivateRoot(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}
func writeChecked(t *testing.T, root *os.Root, name string, body []byte) {
	t.Helper()
	file, err := OpenFile(root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(body); err != nil {
		t.Fatal(err)
	}
}

func TestGenericRootCreatesNoStorageLayout(t *testing.T) {
	base := isolatedRoot(t)
	missing := filepath.Join(base, "missing")
	if root, err := OpenPrivateRoot(missing, false); err == nil {
		root.Close()
		t.Fatal("read-only root created a directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	root, err := OpenPrivateRoot(missing, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	entries, err := os.ReadDir(missing)
	if err != nil || len(entries) != 0 {
		t.Fatalf("generic root created storage-specific entries: %v %v", entries, err)
	}
	child, err := OpenPrivateChild(root, "child", true)
	if err != nil {
		t.Fatal(err)
	}
	child.Close()
	if err := MakePrivateDir(root, "child"); err != nil {
		t.Fatal(err)
	}
}

func TestSafeLeafAndFlagsRefuseBeforeChangingFiles(t *testing.T) {
	root, dir := privateRoot(t)
	writeChecked(t, root, "data", []byte("keep"))
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "/absolute", "C:blob", "blob.", "blob ", "NUL", "COM1"} {
		if file, err := OpenFile(root, name, os.O_RDWR|os.O_CREATE, 0o600); err == nil {
			file.Close()
			t.Fatalf("unsafe leaf accepted: %q", name)
		}
	}
	for _, flags := range []int{os.O_RDWR | os.O_TRUNC, os.O_RDWR | os.O_SYNC, os.O_EXCL, os.O_WRONLY | os.O_RDWR} {
		if file, err := OpenFile(root, "data", flags, 0o600); err == nil {
			file.Close()
			t.Fatalf("unsafe flags accepted: %x", flags)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "data"))
	if err != nil || !bytes.Equal(data, []byte("keep")) {
		t.Fatalf("rejected open changed existing data: %q %v", data, err)
	}
	file, err := OpenFile(root, "data", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if info, err := ValidateFile(root, "data", file, 0o600); err != nil || info.Size() != 0 {
		t.Fatalf("verified descriptor could not be truncated: %v", err)
	}
}

func TestDescriptorValidationRefusesReplacedName(t *testing.T) {
	root, _ := privateRoot(t)
	writeChecked(t, root, "data", []byte("original"))
	file, err := OpenFile(root, "data", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := root.Rename("data", "saved"); err != nil {
		t.Fatal(err)
	}
	writeChecked(t, root, "data", []byte("replacement"))
	if _, err := ValidateDescriptor(file, 0o600); err != nil {
		t.Fatalf("original owned descriptor changed: %v", err)
	}
	if _, err := ValidateFile(root, "data", file, 0o600); err == nil {
		t.Fatal("substituted cache leaf accepted")
	}
	if data, err := root.ReadFile("data"); err != nil || string(data) != "replacement" {
		t.Fatalf("replacement changed: %q %v", data, err)
	}
}

func TestGenericOpenRefusesLinksWithoutForeignCleanup(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root, dir := privateRoot(t)
			foreign := filepath.Join(filepath.Dir(dir), "foreign")
			if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			leaf := filepath.Join(dir, "entry")
			var err error
			if kind == "symlink" {
				err = os.Symlink(foreign, leaf)
			} else {
				err = os.Link(foreign, leaf)
			}
			if err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("fixture link unavailable: %v", err)
				}
				t.Fatal(err)
			}
			before, err := os.Lstat(leaf)
			if err != nil {
				t.Fatal(err)
			}
			if file, err := OpenFile(root, "entry", os.O_RDWR|os.O_CREATE, 0o600); err == nil {
				file.Close()
				t.Fatal("linked cache file accepted")
			}
			after, err := os.Lstat(leaf)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("foreign link removed or replaced: %v", err)
			}
			if data, err := os.ReadFile(foreign); err != nil || string(data) != "keep" {
				t.Fatalf("foreign target changed: %q %v", data, err)
			}
		})
	}
}

func TestExplicitSharedAndExclusiveLeasesExcludeEachOther(t *testing.T) {
	root, _ := privateRoot(t)
	writeChecked(t, root, "lease", nil)
	open := func() *os.File {
		t.Helper()
		file, err := OpenFile(root, "lease", os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { file.Close() })
		return file
	}
	first, second, third := open(), open(), open()
	if held, err := TryLock(first, Shared); err != nil || !held {
		t.Fatalf("first shared lease: %t %v", held, err)
	}
	if held, err := TryLock(second, Shared); err != nil || !held {
		t.Fatalf("second shared lease: %t %v", held, err)
	}
	if held, err := TryLock(third, Exclusive); err != nil || held {
		t.Fatalf("exclusive bypassed shared lease: %t %v", held, err)
	}
	second.Close()
	if held, err := TryLock(third, Exclusive); err != nil || held {
		t.Fatalf("exclusive ignored remaining shared lease: %t %v", held, err)
	}
	first.Close()
	if held, err := TryLock(third, Exclusive); err != nil || !held {
		t.Fatalf("released shared leases did not permit exclusive: %t %v", held, err)
	}
	another := open()
	if held, err := TryLock(another, Shared); err != nil || held {
		t.Fatalf("shared bypassed exclusive lease: %t %v", held, err)
	}
	third.Close()
	if held, err := TryLock(another, Shared); err != nil || !held {
		t.Fatalf("exclusive close did not release lease: %t %v", held, err)
	}
	if _, err := TryLock(another, LockMode(255)); err == nil {
		t.Fatal("unknown lock mode accepted")
	}
}
