//go:build windows

package host

import "os"

// Windows clients never produce a host footprint. The host targets Linux.
func footprintIdentity(_ os.FileInfo) (uint64, uint64, uint64, uint64, bool) {
	return 0, 0, 0, 0, false
}
