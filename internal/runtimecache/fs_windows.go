//go:build windows

package runtimecache

import (
	"golang.org/x/sys/windows"
	"os"
	"time"
)

func availableDisk(f *os.File) (uint64, error) {
	path, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	err = windows.GetDiskFreeSpaceEx(path, &available, &total, &free)
	return available, err
}

func stampArrival(f *os.File, at time.Time) error {
	stamp := windows.NsecToFiletime(at.UnixNano())
	return windows.SetFileTime(windows.Handle(f.Fd()), nil, &stamp, &stamp)
}
