//go:build !windows

package host

import (
	"os"
	"syscall"
)

func footprintIdentity(info os.FileInfo) (device, inode, uid, links uint64, ok bool) {
	if info == nil {
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, 0, false
	}
	return uint64(stat.Dev), stat.Ino, uint64(stat.Uid), uint64(stat.Nlink), true
}
