//go:build !linux

package jail

import (
	"context"
	"io"
	"os"
)

// Cmd is a jail's command. Only Linux has jails, so Command never returns
// one here, and each method returns the error that jails need Linux.
type Cmd struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// errLinuxOnly is the error of every jail off Linux.
var errLinuxOnly = failure("jails need Linux", "Run the host helper on a Linux host.")

// Start returns the error that jails need Linux.
func (*Cmd) Start() error { return errLinuxOnly }

// Wait returns the error that jails need Linux.
func (*Cmd) Wait() error { return errLinuxOnly }

// Run returns the error that jails need Linux.
func (*Cmd) Run() error { return errLinuxOnly }

// CombinedOutput returns the error that jails need Linux.
func (*Cmd) CombinedOutput() ([]byte, error) { return nil, errLinuxOnly }

// StdinPipe returns the error that jails need Linux.
func (*Cmd) StdinPipe() (io.WriteCloser, error) { return nil, errLinuxOnly }

// StdoutPipe returns the error that jails need Linux.
func (*Cmd) StdoutPipe() (io.ReadCloser, error) { return nil, errLinuxOnly }

// StderrPipe returns the error that jails need Linux.
func (*Cmd) StderrPipe() (io.ReadCloser, error) { return nil, errLinuxOnly }

// Signal returns the error that jails need Linux.
func (*Cmd) Signal(os.Signal) error { return errLinuxOnly }

// Pid returns 0: no jail runs here.
func (*Cmd) Pid() int { return 0 }

// ExitCode returns -1: no jail runs here.
func (*Cmd) ExitCode() int { return -1 }

// Init does nothing here: only Linux has jails.
func Init() {}

// Command refuses every jail here: jails need Linux's namespaces, Landlock
// and seccomp.
func Command(_ context.Context, s Spec) (*Cmd, error) {
	if _, err := check(s); err != nil {
		return nil, err
	}
	return nil, errLinuxOnly
}

// Userns reports false here: only Linux has user namespaces.
func Userns() bool { return false }

// RemoveAll removes path and everything in it, as os.RemoveAll does.
func RemoveAll(_ context.Context, path string) error {
	if err := os.RemoveAll(path); err != nil {
		return failure("cannot remove "+quote(path)+": "+err.Error(), "Check the folder's permissions.")
	}
	return nil
}
