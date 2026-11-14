//go:build linux

package process

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// runtimeStorage owns only these runtime objects, never the session's work,
// state, manifest or logs owned by another component. The root pins the actual
// session directory, so replacing its pathname cannot redirect destruction.
type runtimeStorage struct {
	root  *os.Root
	owned map[string]os.FileInfo
}

func openRuntimeStorage(dir string) (*runtimeStorage, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	r := &runtimeStorage{root: root, owned: make(map[string]os.FileInfo)}
	for _, name := range []string{"process.json", "process-root", "namespace.log"} {
		if err := r.capture(name); err != nil {
			return nil, errors.Join(err, root.Close())
		}
	}
	return r, nil
}

func (r *runtimeStorage) capture(name string) error {
	info, err := r.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if name == "process-root" && !info.IsDir() || name != "process-root" && !info.Mode().IsRegular() {
		return fmt.Errorf("runtime object %s has an unsafe type", name)
	}
	if previous := r.owned[name]; previous != nil && !os.SameFile(previous, info) {
		return fmt.Errorf("runtime object %s changed ownership", name)
	}
	r.owned[name] = info
	return nil
}

func (r *runtimeStorage) destroy() error {
	if r == nil || r.root == nil {
		return nil
	}
	dir, err := r.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for _, name := range []string{"namespace.log", "process-root", "process.json"} {
		want := r.owned[name]
		if want == nil {
			continue
		}
		// Claim the name atomically before deleting it. A substituted object
		// is restored without replacement and is never recursively removed.
		staged := ".process-cleanup-" + rand.Text()
		if err := unix.Renameat2(int(dir.Fd()), name, int(dir.Fd()), staged, unix.RENAME_NOREPLACE); err != nil {
			if errors.Is(err, unix.ENOENT) {
				delete(r.owned, name)
				continue
			}
			return fmt.Errorf("claim owned runtime object %s: %w", name, err)
		}
		got, err := r.root.Lstat(staged)
		if err != nil || !os.SameFile(want, got) {
			restoreErr := unix.Renameat2(int(dir.Fd()), staged, int(dir.Fd()), name, unix.RENAME_NOREPLACE)
			return errors.Join(fmt.Errorf("runtime object %s changed before cleanup", name), err, restoreErr)
		}
		if err := r.root.RemoveAll(staged); err != nil {
			// Keep the exact claimed identity for a later cleanup attempt.
			delete(r.owned, name)
			r.owned[staged] = want
			return fmt.Errorf("remove owned runtime object %s: %w", name, err)
		}
		delete(r.owned, name)
	}
	// Retry a previously claimed object after an interrupted removal.
	for name, want := range r.owned {
		got, err := r.root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			delete(r.owned, name)
			continue
		}
		if err != nil || !os.SameFile(want, got) {
			return errors.Join(fmt.Errorf("claimed runtime object changed before cleanup"), err)
		}
		if err := r.root.RemoveAll(name); err != nil {
			return err
		}
		delete(r.owned, name)
	}
	err = r.root.Close()
	r.root = nil
	return err
}
