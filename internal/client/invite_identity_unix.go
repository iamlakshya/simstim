//go:build unix

package client

import (
	"fmt"
	"os"
	"syscall"
)

// joinedNativeIdentity is purpose-private. Generic file validators continue to
// require a single link. Only this original joined file+anchor pair uses two.
func joinedNativeIdentity(file *os.File, links uint64) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || uint64(native.Uid) != uint64(os.Getuid()) || uint64(native.Nlink) != links {
		return "", inviteError("invite_ownership", "a joined file has foreign ownership, mode or link count", "Keep the changed entry and inspect the original host removal.")
	}
	return fmt.Sprintf("unix:%d:%d", uint64(native.Dev), native.Ino), nil
}
