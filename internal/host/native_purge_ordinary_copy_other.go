//go:build !linux

package host

// RunOrdinaryPurgeGitWorkerProcessV3 is the Linux-only raw fixed reader entry.
func RunOrdinaryPurgeGitWorkerProcessV3() int { return 1 }
