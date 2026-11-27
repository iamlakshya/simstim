package host

import "syscall"

// The parent-death chain of a sandbox under the test driver, which has no
// PID namespace: in a real wall, every process of a sandbox dies with its
// guest init, PID 1 of the namespace. Here each process of the chain gets a
// signal when its parent dies.

// DieWithParent makes a child that a sets up get SIGKILL when its parent
// dies: the agent, whose holder ends its group first when it can.
func DieWithParent(a *syscall.SysProcAttr) *syscall.SysProcAttr {
	a.Pdeathsig = syscall.SIGKILL
	return a
}

// StopWithParent makes a child that a sets up get SIGTERM when its parent
// dies, so that it can end its own children first: the guest init and the
// holder.
func StopWithParent(a *syscall.SysProcAttr) *syscall.SysProcAttr {
	a.Pdeathsig = syscall.SIGTERM
	return a
}
