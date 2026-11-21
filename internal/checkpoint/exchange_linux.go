//go:build linux

package checkpoint

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// A successful exchange keeps both original single-link inodes named. The
// boolean records physical success even if closing the directory then fails.
func exchangeFilesV1(root *os.Root, first, second string) (exchanged bool, returned error) {
	dir, err := root.Open(".")
	if err != nil {
		return false, failure("cannot retain the original disk directory for exchange", err)
	}
	defer func() { returned = errors.Join(returned, dir.Close()) }()
	if err := unix.Renameat2(int(dir.Fd()), first, int(dir.Fd()), second, unix.RENAME_EXCHANGE); err != nil {
		return false, fmt.Errorf("exchange the same original stopped disk roles: %w", err)
	}
	return true, nil
}
