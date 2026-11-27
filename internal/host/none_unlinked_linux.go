//go:build linux

package host

import (
	"os"
	"syscall"
)

func checkNoneOwnedDirectoryUnlinkedV1(root *os.Root, created os.FileInfo) error {
	if root == nil || created == nil {
		return sessionLeaseError("the original None unlink has no retained native directory")
	}
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(info, created) || stat.Nlink != 0 {
		return sessionLeaseError("the retained original None directory has no positive native unlink observation")
	}
	return nil
}
