//go:build linux || darwin

package files

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func openRegularFile(root *os.Root, name string) (*os.File, error) {
	return openRegularAccess(root, name, false)
}
func openRegularWriteFile(root *os.Root, name string) (*os.File, error) {
	return openRegularAccess(root, name, true)
}
func openRegularAccess(root *os.Root, name string, writable bool) (*os.File, error) {
	base, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	fd, err := unix.FcntlInt(base.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		if writable && i == len(parts)-1 {
			flags = (flags & ^unix.O_ACCMODE) | unix.O_RDWR
		}
		next, e := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "rooted-regular-file"), nil
}
