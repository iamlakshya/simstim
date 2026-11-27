//go:build !linux && !windows

package client

import (
	"os"

	"golang.org/x/sys/unix"
)

func chmodCollectorSocket(root *os.Root, name string, want os.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil || !agentCollectorSocketOwned(current) || !os.SameFile(current, want) {
		return agentBindingProofError()
	}
	dir, err := root.Open(".")
	if err != nil {
		return agentBindingProofError()
	}
	defer dir.Close()
	if err := unix.Fchmodat(int(dir.Fd()), name, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return agentBindingProofError()
	}
	current, err = root.Lstat(name)
	if err != nil || !os.SameFile(current, want) || current.Mode().Perm() != 0o600 {
		return agentBindingProofError()
	}
	return nil
}
