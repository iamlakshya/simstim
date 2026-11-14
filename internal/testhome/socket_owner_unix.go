//go:build !windows

package testhome

import (
	"os"
	"syscall"
)

func socketDirOwned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid())
}
