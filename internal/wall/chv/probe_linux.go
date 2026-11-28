//go:build linux

package chv

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	kvmGetAPIVersion = 0xae00
	kvmAPIVersion    = 12
)

func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

func checkKVMRW(path string) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	ver, ioErr := unix.IoctlRetInt(fd, kvmGetAPIVersion)
	_ = unix.Close(fd)
	if ioErr != nil {
		return fmt.Errorf("KVM_GET_API_VERSION on %s: %w", path, ioErr)
	}
	if ver != kvmAPIVersion {
		return fmt.Errorf("KVM_GET_API_VERSION returned %d, want %d", ver, kvmAPIVersion)
	}
	return nil
}
