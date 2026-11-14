//go:build windows

package host

import (
	"errors"
	"os"
)

// Runtime installation and host retention are supported on Unix hosts.
func openRuntimeReference(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("runtime retention needs a Unix host")
}
func runtimeReferenceSingleLink(_ os.FileInfo) bool { return false }
