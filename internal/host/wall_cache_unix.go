//go:build !windows

package host

import (
	"os"
	"syscall"
)

func wallCacheOwned(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && uint64(sys.Uid) == uint64(os.Getuid()) //nolint:gosec // G115: Getuid is non-negative on supported hosts.
}
