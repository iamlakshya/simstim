//go:build !linux && !windows

package host

import (
	"os"

	"golang.org/x/sys/unix"
)

func chmodAgentWitness(root *os.Root, name string, want os.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil || current.Mode()&os.ModeSocket == 0 || !os.SameFile(current, want) {
		return agentSocketError()
	}
	dir, err := root.Open(".")
	if err != nil {
		return agentSocketError()
	}
	defer dir.Close()
	if err := unix.Fchmodat(int(dir.Fd()), name, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return agentSocketError()
	}
	current, err = root.Lstat(name)
	if err != nil || !os.SameFile(current, want) || !privateAgentSocket(current) {
		return agentSocketError()
	}
	return nil
}
