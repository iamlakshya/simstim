//go:build !unix

package secretmem_test

// Unsupported platforms supply no child/platform evidence. Protect and
// Inspect return their explicit unsupported-platform errors in production.
func memoryGuardChild() bool { return false }
