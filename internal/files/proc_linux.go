//go:build linux

package files

import (
	"os"

	"golang.org/x/sys/unix"
)

// IsProcDir reports whether fd is on procfs, whose links can carry process
// capabilities rather than ordinary pathname targets.
func IsProcDir(fd *os.File) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(fd.Fd()), &st); err != nil {
		return false, err
	}
	return st.Type == unix.PROC_SUPER_MAGIC, nil
}
