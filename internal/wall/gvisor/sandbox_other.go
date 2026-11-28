//go:build !linux

package gvisor

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
)

// Sandbox is the non-Linux stub for the gVisor wall.
type Sandbox struct {
	spec Spec
}

// NewSandbox creates a non-Linux gVisor sandbox stub.
func NewSandbox(spec Spec) *Sandbox { return &Sandbox{spec: spec} }

// Start returns an unsupported-platform error on non-Linux hosts.
func (*Sandbox) Start(context.Context) (net.Conn, error) {
	return nil, errors.New("gVisor walls require Linux. Try: use a Linux host, or run inside a Lima VM on macOS")
}

// Stop is a no-op on non-Linux hosts.
func (*Sandbox) Stop(context.Context, time.Duration) error { return nil }

// Kill is a no-op on non-Linux hosts.
func (*Sandbox) Kill() error { return nil }

// Wait returns an error exit on non-Linux hosts.
func (*Sandbox) Wait() wall.Exit {
	return wall.Exit{Code: 1, Err: errors.New("gVisor walls require Linux")}
}

// Abilities reports no optional abilities on non-Linux hosts.
func (*Sandbox) Abilities() wall.Abilities { return wall.Abilities{} }

// Destroy is a no-op on non-Linux hosts.
func (*Sandbox) Destroy() error { return nil }

// Stderr returns an empty string on non-Linux hosts.
func (*Sandbox) Stderr() string { return "" }
