//go:build !linux

package host

import (
	"os"
	"strconv"
)

var placementTestProcessToken, _ = footprintNonce()

func placementProcessBirth(pid int) (string, error) {
	if pid == os.Getpid() {
		return strconv.Itoa(pid) + ":" + placementTestProcessToken, nil
	}
	return "", placementError("cross-process placement lifetime proof requires the Linux host kernel")
}
