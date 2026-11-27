//go:build !windows

package client

import (
	"os"
	"syscall"
)

const (
	grantsStateNoFollow = syscall.O_NOFOLLOW
	grantsStateNonblock = syscall.O_NONBLOCK
)

func stateDirectoryOwned(info os.FileInfo) bool {
	return info.Mode().Perm()&0077 == 0 && ownerOf(info) == os.Getuid()
}
func stateBaseDirectoryOwned(info os.FileInfo) bool {
	return info.Mode().Perm() == 0700 && ownerOf(info) == os.Getuid()
}
