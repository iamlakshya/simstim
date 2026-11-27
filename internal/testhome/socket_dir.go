package testhome

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// SocketDir owns one exclusive short directory for a test process. Its raw
// path preserves the system /tmp alias. Its parent and inode are retained so
// a replaced directory cannot be adopted or removed during cleanup.
type SocketDir struct {
	path       string
	parent     string
	parentInfo os.FileInfo
	info       os.FileInfo
}

var activeSocketDir atomic.Pointer[SocketDir]

// NewSocketDir makes a private directory within the supplied path budget.
// Four random bytes retain the standard temporary name's 32 random bits in
// six path safe characters. The caller removes only this exact owned inode.
func NewSocketDir(maxBytes int) (*SocketDir, error) {
	bases := []string{initialTempBase, os.TempDir(), "/tmp"}
	for _, base := range bases {
		base = filepath.Clean(base)
		parent := socketParent(base, maxBytes)
		if parent == nil {
			continue
		}
		return makeSocketDir(base, parent)
	}
	return nil, errors.New("no owned temporary base fits the test socket budget")
}

func socketParent(base string, maxBytes int) os.FileInfo {
	if !filepath.IsAbs(base) || len(filepath.Join(base, "AAAAAA")) > maxBytes {
		return nil
	}
	parent, err := os.Stat(base)
	if err != nil || !parent.IsDir() {
		return nil
	}
	if base == "/tmp" {
		if parent.Mode()&os.ModeSticky == 0 {
			return nil
		}
	} else if !socketDirOwned(parent) || parent.Mode().Perm() != 0o700 {
		return nil
	}
	return parent
}

func makeSocketDir(base string, parent os.FileInfo) (*SocketDir, error) {
	for range 10000 {
		var random [4]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("name the test socket directory: %w", err)
		}
		name := base64.RawURLEncoding.EncodeToString(random[:])
		path := filepath.Join(base, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return nil, fmt.Errorf("make the test socket directory: %w", err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect the new test socket directory: %w", err)
		}
		d := &SocketDir{path: path, parent: base, parentInfo: parent, info: info}
		if err := d.Check(path); err != nil {
			return nil, err
		}
		return d, nil
	}
	return nil, errors.New("no exclusive test socket name remained after 10000 attempts")
}

// Path is the raw short path used at the Unix bind boundary.
func (d *SocketDir) Path() string { return d.path }

// Check verifies the exact directory associated with this test run.
func (d *SocketDir) Check(path string) error {
	if d == nil || filepath.Clean(path) != d.path {
		return errors.New("the control directory is not this run's registered directory")
	}
	parent, err := os.Stat(d.parent)
	if err != nil || !os.SameFile(d.parentInfo, parent) || !parent.IsDir() {
		return errors.New("the test control parent changed")
	}
	if d.parent == "/tmp" {
		if parent.Mode()&os.ModeSticky == 0 {
			return errors.New("the test control parent is no longer sticky")
		}
	} else if !socketDirOwned(parent) || parent.Mode().Perm() != 0o700 {
		return errors.New("the test control parent changed ownership or mode")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !socketDirOwned(info) || !os.SameFile(d.info, info) {
		return errors.New("the test control directory changed ownership, mode or inode")
	}
	return nil
}

// Activate registers this process local directory for isolation assertions.
func (d *SocketDir) Activate() func() {
	old := activeSocketDir.Swap(d)
	return func() { activeSocketDir.Store(old) }
}

// Close checks the retained identity before cleanup, after test workers join.
// A detected replacement is preserved.
func (d *SocketDir) Close() error {
	if err := d.Check(d.path); err != nil {
		return err
	}
	if err := os.RemoveAll(d.path); err != nil {
		return fmt.Errorf("remove the test control directory: %w", err)
	}
	return nil
}

// SocketDirActive reports whether the shared test harness installed a root.
func SocketDirActive() bool { return activeSocketDir.Load() != nil }

// CheckSocketDir keeps the separate SSH control isolation assertion exact.
// A canonical home cannot fit the full 103 byte OpenSSH temporary bind budget.
func CheckSocketDir(t testing.TB, path string) {
	t.Helper()
	d := activeSocketDir.Load()
	if d == nil {
		t.Fatal("the test harness did not register its control directory")
	}
	if err := d.Check(path); err != nil {
		t.Fatal(err)
	}
}
