package host

import "syscall"

// DiskUsage returns the available and total bytes on the file system of path.
func DiskUsage(path string) (avail, total int64, ok bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0, false
	}
	return int64(st.Bavail) * st.Bsize, int64(st.Blocks) * st.Bsize, true //nolint:gosec // G115: filesystem block counts stay far below 2^63
}

// diskFree returns the bytes available to the user on the file system of path.
func diskFree(path string) int64 {
	avail, _, _ := DiskUsage(path)
	return avail
}
