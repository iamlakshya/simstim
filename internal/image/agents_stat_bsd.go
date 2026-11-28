//go:build darwin || freebsd || netbsd || openbsd

package image

import (
	"os"
	"syscall"
)

func statIdentity(fi os.FileInfo) (ctimeNS int64, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st != nil {
		return st.Ctimespec.Nano(), st.Ino
	}
	return 0, 0
}
