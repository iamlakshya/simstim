//go:build unix

package client

import (
	"os"
	"syscall"
)

func openImageCredentialFile(path string) (configFile, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
