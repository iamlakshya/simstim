//go:build linux

package host

import "os"

// Reuse ONLY the existing read-only native FLOCK observation primitive. This
// constructs no ordinary holder/source and changes no ordinary/OP predicate.
func observeNoneHolderV1(file *os.File) error { return checkOrdinaryHolderFlockV1(file) }
