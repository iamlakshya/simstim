//go:build !linux

package host

import "os"

func checkNoneOwnedDirectoryUnlinkedV1(*os.Root, os.FileInfo) error {
	return sessionLeaseError("the original None native unlink observer requires its Linux host")
}
