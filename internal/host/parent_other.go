//go:build !linux

package host

import "syscall"

// DieWithParent returns a as it is: only Linux can tie a child's life to
// its parent's. Off Linux, the test driver reaches only what each process
// ends of its own children (docs/explanation/architecture.md, 2.5).
func DieWithParent(a *syscall.SysProcAttr) *syscall.SysProcAttr { return a }

// StopWithParent returns a as it is, as DieWithParent does.
func StopWithParent(a *syscall.SysProcAttr) *syscall.SysProcAttr { return a }
