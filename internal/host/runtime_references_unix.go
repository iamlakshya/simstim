//go:build !windows

package host

import (
	"os"
	"syscall"
)

func openRuntimeReference(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
func runtimeReferenceSingleLink(st os.FileInfo) bool {
	v, ok := st.Sys().(*syscall.Stat_t)
	return ok && v.Nlink == 1
}
