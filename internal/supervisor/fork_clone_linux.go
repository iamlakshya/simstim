//go:build linux

package supervisor

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func cloneForkFileV1(destination, source *os.File) (bool, error) {
	err := unix.IoctlFileClone(int(destination.Fd()), int(source.Fd()))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EINVAL) {
		return false, nil
	}
	return false, err
}
