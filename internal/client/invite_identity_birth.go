package client

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This creator-only object is never serialised or reconstructed from matching
// bytes/pathnames. A failed invite store can undo only its actual new key.
type inviteIdentityBirth struct {
	path      string
	info      os.FileInfo
	directory cachefs.Reference
}

func (b *inviteIdentityBirth) remove() error {
	if b == nil {
		return nil
	}
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(b.path), false)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = b.directory.CheckRoot(root); err != nil {
		return err
	}
	leaf := filepath.Base(b.path)
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := file.Stat()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if !os.SameFile(b.info, info) {
		return inviteError("invite_key", "the new host key was replaced, so it was kept", "Inspect the original invite's key before retrying.")
	}
	current, err := root.Lstat(leaf)
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return inviteError("invite_key", "the new host key changed before cleanup, so it was kept", "Inspect the key before retrying.")
	}
	return root.Remove(leaf)
}
