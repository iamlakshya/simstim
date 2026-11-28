//go:build !linux

package process

import "errors"

// SeccompBPF is only available on Linux hosts.
func SeccompBPF() ([]byte, error) {
	return nil, errors.New("seccomp requires Linux. Try: run on a Linux host or inside a Lima VM")
}
