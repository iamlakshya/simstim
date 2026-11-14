package host

import (
	"context"
	"errors"
	"os"
)

// A running session's rename (docs/contributing/cli-spec.md 9.5). The
// holder renames under the session's lock, op.lock, which a delete holds
// across its check of the name and the delete. It writes the manifest
// first, and changes the name in memory only after the write: a rename
// that answers yes is in the manifest, and a rename that comes late, as
// after its caller gave up waiting, still waits for the lock.

// errEnding is the error of a rename while the holder makes its last save.
var errEnding = errors.New("the session is ending")

// beforeRenameLock runs in the holder's rename before it takes the lock.
// The tests of this package stall a rename there.
var beforeRenameLock = func(string) {}

// afterRenameWrite runs in the holder's rename after its write, before
// the name changes in memory. Tests take a tick's copy there.
var afterRenameWrite = func(*Holder) {}

// afterHolderLoad runs in a new holder after it loads the manifest, before
// it launches the agent. The early control endpoint already exists. Tests
// stall at this same boundary and can observe cancellation without moving it.
var afterHolderLoad = func(context.Context, string) {}

// rename renames the session to name. It takes the session's lock, saves a
// copy of the manifest with the new name, and only after a good write
// changes the name in memory and records the copy as saved. A failed write
// changes neither, and is the error. A copy that a tick took during the
// write holds the old name. It counts as older than the rename, so its
// save writes the session as it is now, with the new name (write).
func (h *Holder) rename(name string) error {
	beforeRenameLock(h.id)
	unlock, err := lockSessionWait(h.id, renameWait)
	if err != nil {
		return err
	}
	defer unlock()
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	if h.closed {
		return errEnding
	}
	h.mu.Lock()
	c := h.copyForSave()
	h.mu.Unlock()
	c.m.Name = name
	if err := c.m.Save(); err != nil {
		return err
	}
	_ = os.Remove(unsavedPath(h.id))
	afterRenameWrite(h)
	h.mu.Lock()
	h.m.Name = name
	h.saved = h.copies
	h.mu.Unlock()
	return nil
}
