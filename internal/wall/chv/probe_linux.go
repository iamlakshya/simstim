//go:build linux

package chv

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

func checkKVMRW(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	_ = f.Close()
	return nil
}
