//go:build !darwin && !linux

package diag

// Platforms without the shared Linux/macOS clock contract keep a clock
// relative to this process. Those records cannot calibrate another host.
const perCallMono = false
