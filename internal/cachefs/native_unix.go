//go:build unix

package cachefs

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func owned(st fs.FileInfo) bool {
	v, ok := st.Sys().(*syscall.Stat_t)
	return ok && uint64(v.Uid) == uint64(os.Getuid()) //nolint:gosec // Getuid is non-negative on supported systems.
}

func safeDirectory(_ *os.Root, _ string, st fs.FileInfo, private bool) bool {
	if st == nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	v, ok := st.Sys().(*syscall.Stat_t)
	if !ok || (v.Uid != 0 && !owned(st)) || (private && !owned(st)) {
		return false
	}
	return st.Mode().Perm()&0o022 == 0 || (!private && v.Uid == 0 && st.Mode()&os.ModeSticky != 0)
}

func openNoFollow(root *os.Root, name string, flags int, mode fs.FileMode) (*os.File, error) {
	return root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
}

func tryLock(f *os.File, mode LockMode) (bool, error) {
	flags := unix.LOCK_EX | unix.LOCK_NB
	if mode == Shared {
		flags = unix.LOCK_SH | unix.LOCK_NB
	}
	err := unix.Flock(int(f.Fd()), flags)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func syncDirectory(f *os.File) error { return f.Sync() }

func privateMode(st fs.FileInfo) bool                   { return st.Mode().Perm() == 0o700 }
func modeMatches(st fs.FileInfo, mode fs.FileMode) bool { return st.Mode().Perm() == mode }
func descriptorSafe(_ *os.File, st fs.FileInfo) bool {
	v, ok := st.Sys().(*syscall.Stat_t)
	return ok && owned(st) && v.Nlink == 1
}
