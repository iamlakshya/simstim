//go:build unix

package supply

import (
	"os"
	"syscall"
)

func singleLink(_ *os.File, st os.FileInfo) bool {
	v, ok := st.Sys().(*syscall.Stat_t)
	return ok && v.Nlink == 1
}
