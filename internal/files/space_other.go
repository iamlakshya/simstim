//go:build !linux && !darwin && !windows

package files

import (
	"fmt"
	"os"
)

// SpaceAt refuses platforms outside the supported host and client systems.
func SpaceAt(_ *os.Root) (Space, error) {
	return Space{}, fmt.Errorf("filesystem accounting is unavailable on this platform")
}
