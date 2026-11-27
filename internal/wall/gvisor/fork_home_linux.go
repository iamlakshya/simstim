//go:build linux

package gvisor

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// freshForkHomeV1 belongs only to this original prepared factory. It is never
// reopened or adopted from an old runtime layout, ID, manifest or saved path.
type freshForkHomeV1 struct {
	mu        sync.Mutex
	owner     *runtimeStorage
	root      *os.Root
	file      *os.File
	reference cachefs.Reference
	name      string
	closed    bool
	claimed   bool
	removed   bool
	closeErr  error
}

func createFreshForkHomeV1(storage *runtimeStorage) (_ *freshForkHomeV1, returned error) {
	if storage == nil || storage.root == nil || storage.session == nil || storage.lease == nil {
		return nil, failure("wall_setup", "the fresh gvisor home has no original storage factory", nil)
	}
	home := &freshForkHomeV1{owner: storage, name: "home-" + rand.Text()}
	// An exclusive successful mkdir is the only creation receipt. An existing
	// random name is never adopted or chmodded into this factory's ownership.
	if err := storage.root.Mkdir(home.name, 0o700); err != nil {
		return nil, failure("wall_setup", "cannot create a fresh private gvisor home", err)
	}
	var err error
	home.root, err = cachefs.OpenPrivateChild(storage.root, home.name, false)
	if err != nil {
		return home, err // Preserve a creation whose identity was not captured.
	}
	home.reference, err = cachefs.CaptureRoot(home.root)
	if err != nil {
		return home, err
	}
	home.file, err = home.root.Open(".")
	if err != nil {
		return home, err
	}
	if err := home.check(); err != nil {
		return home, err
	}
	return home, nil
}

func (home *freshForkHomeV1) check() error {
	if home == nil {
		return failure("wall_setup", "the original fresh gvisor home owner ended", nil)
	}
	home.mu.Lock()
	defer home.mu.Unlock()
	return home.checkLockedV1()
}

func (home *freshForkHomeV1) checkLockedV1() error {
	if home.closed || home.removed || home.owner == nil || home.owner.root == nil || home.root == nil || home.file == nil {
		return failure("wall_setup", "the original fresh gvisor home owner ended", nil)
	}
	if err := home.reference.CheckRoot(home.root); err != nil {
		return err
	}
	actual, err := home.file.Stat()
	if err != nil {
		return err
	}
	named, err := home.owner.root.Lstat(home.name)
	if err != nil || !named.IsDir() || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(actual, named) || named.Mode().Perm() != 0o700 {
		return errors.Join(fmt.Errorf("the original gvisor home changed native identity or mode"), err)
	}
	return nil
}

func (home *freshForkHomeV1) close() error {
	if home == nil {
		return nil
	}
	home.mu.Lock()
	defer home.mu.Unlock()
	if home.closed || home.closeErr != nil {
		return home.closeErr
	}
	if !home.removed {
		if err := home.checkLockedV1(); err != nil {
			return err
		}
		if !home.claimed {
			claimed := "home-cleanup-" + rand.Text()
			if err := cachefs.RenameNoReplace(home.owner.root, home.name, claimed); err != nil {
				return err
			}
			home.name, home.claimed = claimed, true
		}
		if err := home.checkLockedV1(); err != nil {
			return err // Retain the claimed SAME creation for its actual owner.
		}
		if err := home.owner.root.RemoveAll(home.name); err != nil {
			return err
		}
		home.removed = true // Actual successful removal, never Close errno.
	}
	home.closeErr = errors.Join(home.file.Close(), home.root.Close())
	home.closed = home.closeErr == nil
	return home.closeErr
}
