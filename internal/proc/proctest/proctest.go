//go:build linux || darwin

// Package proctest gives tests a hold on a process, so that a signal of
// the test reaches that process only, never a later process with the same
// ID: the tests follow rule B too (docs/explanation/architecture.md, 2.5).
// Only tests import it. A test signals a process that it started through
// its own os.Process, and any other process through a Handle.
package proctest

import (
	"errors"
	"syscall"
)

// ErrNoHold is the error of a signal through a Handle that holds no
// process any more.
var ErrNoHold = errors.New("proctest: the process has gone, or is no longer held")

// Pin returns a hold on the live process pid, once it has checked that
// the process's command line holds word. It returns nil when the process
// has gone, runs something else, or cannot be held on this system: off
// Linux, a process that is not this process's unreaped child.
func Pin(pid int, word string) *Handle { return pin(pid, word) }

// Signal sends sig to the held process, or fails with ErrNoHold once it is
// gone.
func (h *Handle) Signal(sig syscall.Signal) error { return h.signal(sig) }

// Alive reports whether the held process still runs, or waits as a zombie.
func (h *Handle) Alive() bool { return h.signal(0) == nil }

// Close lets the process go.
func (h *Handle) Close() { h.close() }

// Reaped reports whether pid is no child of this process that waits to be
// reaped: its parent here has reaped it, or it was never a child. It reaps
// nothing.
func Reaped(pid int) bool { return !unreapedChild(pid) }
