package cachefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreatedRootReportsOnlyThisActualMkdir(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(base, "a"), filepath.Join(base, "a", "b")
	var made []string
	root, err := OpenPrivateRootCreated(b, func(path string, info os.FileInfo) error {
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatal("created receipt has no actual private directory", info)
		}
		made = append(made, path)
		if path == a {
			return os.Mkdir(b, 0o700)
		} // A different creator owns b.
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if len(made) != 1 || made[0] != a {
		t.Fatal("another creator's directory was adopted", made)
	}
	root, err = OpenPrivateRootCreated(b, func(string, os.FileInfo) error { t.Fatal("existing directory received a birth receipt"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
}

func TestCreatedRootObserverStopsBeforeTheNextChild(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("original observer stopped")
	_, err = OpenPrivateRootCreated(filepath.Join(base, "a", "b"), func(string, os.FileInfo) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatal("observer failure was lost", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "a", "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("factory created a later child after its producer ended", err)
	}
}

func TestCreatedChildRequiresObserver(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenPrivateRoot(filepath.Join(base, "root"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if child, err := OpenPrivateChildCreated(root, "child", nil); err == nil {
		child.Close()
		t.Fatal("OpenPrivateChildCreated accepted nil observer")
	}
	if _, err := root.Lstat("child"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nil observer still created child directory: %v", err)
	}
}
