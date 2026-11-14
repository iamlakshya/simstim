//go:build linux

package cachefs

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameBetweenNoReplaceNative(root *os.Root, from string, target *os.Root, to string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	destination, err := target.Open(".")
	if err != nil {
		return err
	}
	defer destination.Close()
	return unix.Renameat2(int(dir.Fd()), from, int(destination.Fd()), to, unix.RENAME_NOREPLACE)
}
