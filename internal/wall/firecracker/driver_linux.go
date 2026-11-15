//go:build linux

package firecracker

import "github.com/iamlakshya/simstim/internal/wall/microvm"

// Options binds the same host-owned resource and publication dependencies as
// every microvm backend. It contains no guest-selected runtime source.
type Options = microvm.Options

// Driver is the shared engine configured for Firecracker's MMIO backend.
type Driver = microvm.Driver

// New constructs the real backend without starting a process or changing a host.
func New(options Options) (*Driver, error) { return microvm.NewForDriver("firecracker", options) }
