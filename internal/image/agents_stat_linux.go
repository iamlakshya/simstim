//go:build linux

package image

import (
	"os"
	"syscall"
)

func statIdentity(fi os.FileInfo) (ctimeNS int64, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st != nil {
		return st.Ctim.Nano(), st.Ino
	}
	return 0, 0
}
