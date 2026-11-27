package host

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// cleanupUninitialisedGuard removes only this failed constructor's exclusive
// uncommitted domain. A published control or unknown node keeps the directory.
// A crash before registration remains an explicitly uncertain orphan.
func (p *placement) cleanupUninitialisedGuard(home *os.Root) error {
	root, err := cachefs.OpenPrivateChild(home, filepath.Base(p.control), false)
	if err != nil {
		return err
	}
	defer root.Close()
	if p.controlRef == nil {
		return placementError("the failed constructor has no directory cleanup witness")
	}
	if err := refOf(p.controlRef).matchesRoot(root); err != nil {
		return err
	}
	if _, err := root.Lstat("control.json"); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "control.lock" || p.controlLockRef == nil {
			return placementError("the failed constructor preserved unknown guard children")
		}
		file, err := cachefs.OpenFile(root, "control.lock", os.O_RDONLY, 0o600)
		if err != nil {
			return err
		}
		info, err := cachefs.ValidateFile(root, "control.lock", file, 0o600)
		if err != nil || !refOf(p.controlLockRef).matches(info) {
			file.Close()
			return placementError("the uncommitted constructor sentinel changed")
		}
		if err := root.Remove("control.lock"); err != nil {
			file.Close()
			return err
		}
		file.Close()
	}
	if err := refOf(p.controlRef).matchesRoot(root); err != nil {
		return err
	}
	if err := home.Remove(filepath.Base(p.control)); err != nil {
		return err
	}
	return syncCapturedDirectory(home)
}
