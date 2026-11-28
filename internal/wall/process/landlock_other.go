//go:build !linux

package process

import "errors"

// ApplyLandlock is only available on Linux hosts.
func ApplyLandlock(LandlockConfig) error {
	return errors.New("landlock requires Linux. Try: run on a Linux host or inside a Lima VM")
}
