//go:build !linux

package host

// Linux owns the native copied-image boundary. Other helpers cannot enter it.
func RunPurgeAttemptProcessV3() int { return 1 }
