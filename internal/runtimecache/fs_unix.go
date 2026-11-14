//go:build unix

package runtimecache

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"time"
)

func availableDisk(f *os.File) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	if st.Bsize <= 0 {
		return 0, errors.New("file system reports an invalid block size")
	}
	blocks, size := uint64(st.Bavail), uint64(st.Bsize)
	if blocks > ^uint64(0)/size {
		return ^uint64(0), nil
	}
	return blocks * size, nil
}

func stampArrival(f *os.File, at time.Time) error {
	stamp := unix.NsecToTimeval(at.UnixNano())
	return unix.Futimes(int(f.Fd()), []unix.Timeval{stamp, stamp})
}
