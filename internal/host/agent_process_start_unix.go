//go:build !windows

package host

import (
	"os/signal"
	"syscall"

	"github.com/iamlakshya/simstim/internal/secretmem"
)

// This hook belongs only to the dedicated process, before its first byte.
// EPIPE must reach the owner so leases revoke before the process exits.
func prepareAgentProcess() error {
	signal.Ignore(syscall.SIGPIPE)
	return secretmem.Protect()
}
