package client

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func chmodCollectorSocket(root *os.Root, name string, want os.FileInfo) error {
	dir, err := root.Open(".")
	if err != nil {
		return agentBindingProofError()
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return agentBindingProofError()
	}
	file := os.NewFile(uintptr(fd), "agent binding socket")
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !agentCollectorSocketOwned(current) || !os.SameFile(current, want) {
		return agentBindingProofError()
	}
	err = unix.Fchmodat(fd, "", 0o600, unix.AT_EMPTY_PATH)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		err = unix.Fchmodat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), 0o600, 0)
	}
	if err != nil {
		return agentBindingProofError()
	}
	return nil
}
