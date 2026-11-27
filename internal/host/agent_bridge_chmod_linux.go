package host

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// chmodAgentWitness opens the retained inode without following links.
// Older Linux kernels use the same held descriptor through procfs.
func chmodAgentWitness(root *os.Root, name string, want os.FileInfo) error {
	dir, err := root.Open(".")
	if err != nil {
		return agentSocketError()
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return agentSocketError()
	}
	file := os.NewFile(uintptr(fd), "agent socket witness")
	defer file.Close()
	got, err := file.Stat()
	if err != nil || got.Mode()&os.ModeSocket == 0 || !os.SameFile(got, want) {
		return agentSocketError()
	}
	err = unix.Fchmodat(fd, "", 0o600, unix.AT_EMPTY_PATH)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		// The descriptor remains open until after chmod. No mutable socket
		// pathname is resolved by this procfs descriptor reference.
		err = unix.Fchmodat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), 0o600, 0)
	}
	if err != nil {
		return agentSocketError()
	}
	return nil
}
