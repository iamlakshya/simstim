package chv

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	mib               int64 = 1 << 20
	fixedReserveBytes int64 = 128 * mib
	pageTableDivisor  int64 = 256
)

// Reserve returns the host-side memory reserve outside guest RAM for a microvm
// with guestBytes of RAM (isolation spec 20.10, decision 0040):
//
//	reserve = 128 MiB + guestBytes / 256, rounded up to a whole MiB.
func Reserve(guestBytes int64) int64 {
	if guestBytes <= 0 {
		return fixedReserveBytes
	}
	// ceil(guestBytes / 256), then rounded up to a whole MiB.
	ptBytes := (guestBytes + pageTableDivisor - 1) / pageTableDivisor
	ptMiB := (ptBytes + mib - 1) / mib
	return fixedReserveBytes + ptMiB*mib
}

// UnitMemoryMax returns the systemd unit MemoryMax for a microvm with
// guestBytes of guest RAM (isolation spec 20.10, decision 0040).
func UnitMemoryMax(guestBytes int64) int64 {
	return guestBytes + Reserve(guestBytes)
}

// GuestMemoryMiB validates guestBytes and returns the whole MiB passed to
// Cloud Hypervisor's --memory size=<n>M flag.
func GuestMemoryMiB(guestBytes int64) (int64, error) {
	if guestBytes < api.MinMemoryMax {
		return 0, fmt.Errorf("guest memory %d MiB is below the 256 MiB minimum. Try: set --memory-max to 256M or higher", guestBytes/mib)
	}
	return (guestBytes + mib - 1) / mib, nil
}
