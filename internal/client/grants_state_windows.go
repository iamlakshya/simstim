//go:build windows

package client

import "os"

const (
	grantsStateNoFollow = 0
	grantsStateNonblock = 0
)

func stateDirectoryOwned(os.FileInfo) bool     { return true }
func stateBaseDirectoryOwned(os.FileInfo) bool { return true }
