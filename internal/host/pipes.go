package host

import (
	"os/signal"
	"syscall"
)

// KeepOnBrokenPipe keeps this long-lived helper running when one client pipe
// closes. The failed write still reports its I/O error to its owning request.
// It changes only this process's signal disposition, not another process.
func KeepOnBrokenPipe() { signal.Ignore(syscall.SIGPIPE) }
