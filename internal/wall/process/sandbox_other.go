//go:build !linux

package process

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
)

// Sandbox is a stub on non-Linux hosts; the bubblewrap process wall runs on Linux.
type Sandbox struct{}

// NewSandbox creates a non-Linux stub sandbox.
func NewSandbox(Spec) *Sandbox { return &Sandbox{} }

// Start returns an actionable error on non-Linux hosts.
func (*Sandbox) Start(context.Context) (net.Conn, error) {
	return nil, errors.New("the process wall requires Linux. Try: use a Linux host or a Lima VM")
}

// Stop is a no-op on non-Linux hosts.
func (*Sandbox) Stop(context.Context, time.Duration) error { return nil }

// Kill is a no-op on non-Linux hosts.
func (*Sandbox) Kill() error { return nil }

// Wait returns an error exit on non-Linux hosts.
func (*Sandbox) Wait() wall.Exit {
	return wall.Exit{Code: 1, Err: errors.New("the process wall requires Linux")}
}

// Destroy is a no-op on non-Linux hosts.
func (*Sandbox) Destroy() error { return nil }

// Abilities reports optional sandbox abilities for the process wall.
func (*Sandbox) Abilities() wall.Abilities { return wall.Abilities{} }

// Stderr returns empty text on non-Linux hosts.
func (*Sandbox) Stderr() string { return "" }

// BwrapVersion returns "" on non-Linux hosts.
func BwrapVersion(context.Context, string) string { return "" }
