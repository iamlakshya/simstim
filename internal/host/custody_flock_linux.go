//go:build linux

package host

import "os"

// checkCustodyFlockV1 observes the SAME previously locked open description.
// Its original locker may have exited. The shared kernel observation parser
// supplies no ordinary Source and never acquires, unlocks or reopens a lock.
// Live op.lock loans retain their separate current-sender PID predicate.
func checkCustodyFlockV1(file *os.File) error {
	return checkOrdinaryHolderFlockV1(file)
}
