package client

import (
	"context"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"os"
	"path/filepath"
)

// The invite lock is separate from the host OP. H10's post-registration saved
// Source phases start from a clean context, and never inherit a registration OP.
func lockInvite(ctx context.Context, name string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(StateDir(), "invites")
	if err := privateInviteDir(dir); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	leaf := HostFileKey(name) + ".lock"
	f, err := cachefs.OpenFile(root, leaf, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	closeFile := true
	defer func() {
		if closeFile {
			f.Close()
		}
	}()
	st, err := root.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || !os.SameFile(st, opened) {
		return nil, inviteError("invite_store", "the invite lock changed or is not private", "Check the invite folder's ownership and permissions.")
	}
	locked, err := tryLockFile(f, true)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, inviteError("host_busy", "another command owns this host's invite", "Wait for the original command to finish.")
	}
	if err = ctx.Err(); err != nil {
		_ = unlockFile(f)
		return nil, err
	}
	closeFile = false
	return func() { _ = unlockFile(f); _ = f.Close() }, nil
}
