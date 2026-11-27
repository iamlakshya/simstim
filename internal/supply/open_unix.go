//go:build unix

package supply

import (
	"fmt"
	"os"
	"syscall"
)

func openReadOnly(root *os.Root, p string) (*os.File, error) {
	// A path swapped to a FIFO must not block before openRegular can inspect
	// the descriptor. A final symlink must not be followed after Lstat.
	f, err := root.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", p, err)
	}
	return f, nil
}
