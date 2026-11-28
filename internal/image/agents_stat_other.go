//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package image

import "os"

func statIdentity(os.FileInfo) (ctimeNS int64, ino uint64) { return 0, 0 }
