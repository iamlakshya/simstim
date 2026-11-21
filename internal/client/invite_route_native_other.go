//go:build !unix && !windows

package client

import "os"

func routeConfigNativeSafeV1(*os.File, os.FileInfo) error {
	return routeSourceErrorV1("this platform cannot prove explicit SSH source identity")
}

func routeNativeIDV1(*os.File) (string, error) {
	return "", routeSourceErrorV1("the native source identity is unavailable")
}

func routeSocketNativeIDV1(os.FileInfo) (string, error) {
	return "", routeSourceErrorV1("this platform cannot retain a native SSH agent socket for this route")
}
func routeDirectoryNativeSafeV1(file *os.File, info os.FileInfo) error {
	if !info.IsDir() {
		return routeSourceErrorV1("an SSH source directory is not a directory")
	}
	return nil
}
