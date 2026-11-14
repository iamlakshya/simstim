//go:build windows

package testhome

import "os"

func socketDirOwned(os.FileInfo) bool { return false }
