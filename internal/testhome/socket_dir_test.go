//go:build !windows

package testhome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControlRootHasAnExclusiveRunIdentity(t *testing.T) {
	first, err := NewSocketDir(15)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	})
	second, err := NewSocketDir(15)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})
	if first.Path() == second.Path() || len(first.Path()) > 15 || len(second.Path()) > 15 {
		t.Fatal("the control roots were shared or exceeded the full bind budget")
	}
	if err := first.Check(second.Path()); err == nil {
		t.Fatal("one run accepted another run's directory")
	}
	restore := first.Activate()
	defer restore()
	CheckSocketDir(t, first.Path())
	info, err := os.Stat(first.Path())
	if err != nil || !socketDirOwned(info) || info.Mode().Perm() != 0o700 {
		t.Fatalf("the run's root is not private: %v", err)
	}
}

func TestControlRootRejectsReplacementAndModeChanges(t *testing.T) {
	d, err := NewSocketDir(15)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(d.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.Check(d.Path()); err == nil {
		t.Error("the run accepted a public root")
	}
	if err := os.Chmod(d.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	held := d.Path() + "-held"
	if err := os.Rename(d.Path(), held); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(d.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(d.Path(), "foreign")
	if err := os.WriteFile(sentinel, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err == nil {
		t.Error("cleanup adopted a replacement inode")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("cleanup removed the replacement: %v", err)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(d.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(held, d.Path()); err != nil {
		t.Fatal(err)
	}
}
