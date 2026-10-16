//go:build darwin

package diag

// perCallMono makes Now read CLOCK_MONOTONIC at each call. On macOS, Go's
// own monotonic clock is mach_absolute_time, which drifts from
// CLOCK_MONOTONIC by some µs a minute. Two processes that started apart
// would then disagree by more than a key's hop between them.
const perCallMono = true
