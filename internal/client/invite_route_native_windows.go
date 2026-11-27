//go:build windows

package client

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func routeConfigNativeSafeV1(file *os.File, info os.FileInfo) error {
	var native windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &native); err != nil {
		return err
	}
	if !info.Mode().IsRegular() || native.NumberOfLinks != 1 || !joinedPrivateACL(file) {
		return routeSourceErrorV1("an SSH source has unsafe native ACLs")
	}
	return nil
}

func routeNativeIDV1(file *os.File) (string, error) {
	var native windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &native); err != nil {
		return "", err
	}
	return fmt.Sprintf("windows:%d:%d:%d", native.VolumeSerialNumber, native.FileIndexHigh, native.FileIndexLow), nil
}

func routeSocketNativeIDV1(os.FileInfo) (string, error) {
	return "", routeSourceErrorV1("this platform cannot retain a native SSH agent socket for this route")
}
func routeDirectoryNativeSafeV1(file *os.File, info os.FileInfo) error {
	if !info.IsDir() || !joinedPrivateACL(file) {
		return routeSourceErrorV1("an SSH source directory is not a directory")
	}
	return nil
}
