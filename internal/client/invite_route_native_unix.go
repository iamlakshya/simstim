//go:build unix

package client

import (
	"fmt"
	"os"
	"syscall"
)

func routeConfigNativeSafeV1(file *os.File, info os.FileInfo) error {
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || (native.Uid != 0 && uint64(native.Uid) != uint64(os.Getuid())) || uint64(native.Nlink) != 1 {
		return routeSourceErrorV1("an SSH source has unsafe native owner, mode or link count")
	}
	return nil
}

func routeNativeIDV1(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", routeSourceErrorV1("the native source identity is unavailable")
	}
	return fmt.Sprintf("unix:%d:%d", uint64(native.Dev), native.Ino), nil
}

func routeSocketNativeIDV1(info os.FileInfo) (string, error) {
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 || uint64(native.Uid) != uint64(os.Getuid()) || uint64(native.Nlink) != 1 {
		return "", routeSourceErrorV1("the original SSH agent socket has unsafe native identity")
	}
	return fmt.Sprintf("unix-socket:%d:%d", uint64(native.Dev), native.Ino), nil
}
func routeDirectoryNativeSafeV1(file *os.File, info os.FileInfo) error {
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o022 != 0 || (native.Uid != 0 && uint64(native.Uid) != uint64(os.Getuid())) {
		return routeSourceErrorV1("an SSH source directory has unsafe native owner or mode")
	}
	return nil
}
