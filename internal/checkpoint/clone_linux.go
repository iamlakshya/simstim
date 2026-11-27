//go:build linux

package checkpoint

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func cloneFile(target, source *os.File) error {
	if err := unix.IoctlFileClone(int(target.Fd()), int(source.Fd())); err != nil {
		return fmt.Errorf("clone the original disk descriptor: %w", err)
	}
	return nil
}

func cloneUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOSYS)
}
