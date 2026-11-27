//go:build !windows

package testhome

import (
	"math"
	"os"
	"syscall"
)

func socketDirOwned(info os.FileInfo) bool {
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > math.MaxUint32 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(uid)
}
