//go:build linux

// Package nitro runs direct-mode Nitro Sandboxes through the fixed signed
// Firecracker runtime and the shared VM lifecycle. It never calls EC2 APIs.
package nitro

import "github.com/iamlakshya/simstim/internal/wall/microvm"

// Options supplies canonical host facts, storage and protected publication.
type Options = microvm.Options

// Driver uses the shared resource engine with the explicit Nitro PCI backend.
type Driver = microvm.Driver

// New binds dependencies without starting a process or changing the parent.
func New(options Options) (*Driver, error) { return microvm.NewForDriver("nitro", options) }
