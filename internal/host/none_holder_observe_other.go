//go:build !linux

package host

import "os"

func observeNoneHolderV1(*os.File) error {
	return sessionLeaseError("the original None holder observation requires its Linux host")
}
