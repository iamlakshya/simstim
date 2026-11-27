//go:build darwin

package hosttest

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneOrCopy uses APFS clonefile for an instant copy with a distinct inode
// (Nlink == 1, required by placement verification), falling back to copyFileBytes.
func cloneOrCopy(src, dst string) error {
	_ = os.Remove(dst)
	if err := unix.Clonefile(src, dst, 0); err == nil {
		return os.Chmod(dst, 0o755)
	}
	return copyFileBytes(src, dst)
}
