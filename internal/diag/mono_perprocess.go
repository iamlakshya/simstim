//go:build !darwin

package diag

// perCallMono is false here: on Linux, Go's own monotonic clock is
// CLOCK_MONOTONIC, through the vDSO, so the start's reading plus Go's
// clock stays on the system's clock, and costs no system call.
const perCallMono = false
