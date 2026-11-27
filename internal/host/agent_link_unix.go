//go:build !windows

package host

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	agentNoFollow = unix.O_NOFOLLOW
	agentNonblock = unix.O_NONBLOCK
)

func privateAgentSocket(st os.FileInfo) bool {
	owner, ok := st.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid()) && st.Mode().Perm()&0o077 == 0
}

func privateAgentLock(st os.FileInfo) bool {
	if st == nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		return false
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid())
}

func tryAgentLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func releaseAgentLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
