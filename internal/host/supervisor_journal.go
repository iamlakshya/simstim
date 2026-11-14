package host

import (
	"errors"
	"os"
	"time"
)

// SavedRenameFeature is the canonical persisted-rename capability.
const SavedRenameFeature = renameSaves

// AcquireSupervisorLease uses the holder's existing kernel ownership and
// closes its inherited launch descriptor on exec before any children start.
func AcquireSupervisorLease(id string) (func(), error) { return acquireHolderLock(id) }

// WithSessionLease uses the same operation lease as rename, resume and delete.
func WithSessionLease(id string, wait time.Duration, run func() error) error {
	unlock, err := lockSessionWait(id, wait)
	if err != nil {
		return err
	}
	defer unlock()
	return run()
}

// WithFirstManifestLease serialises a late supervisor's first save with the
// accepted first-save/name/delete protocol. A removed session is never revived.
func WithFirstManifestLease(id string, run func(*Manifest) error) (bool, error) {
	unlock, err := lockFirstSave(id, renameWait)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || sessionGone(id) {
			return false, nil
		}
		return false, err
	}
	defer unlock()
	if sessionGone(id) {
		return false, nil
	}
	disk, err := LoadManifest(id)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, run(disk)
}

// ClearUnsavedRename removes only the canonical older-holder rename marker.
func ClearUnsavedRename(id string) { _ = os.Remove(unsavedPath(id)) }
