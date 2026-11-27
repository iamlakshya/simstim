//go:build linux

package guest

import "syscall"

// Docker is explicitly guest-root. This helper is reachable only after the
// actual PID1/root VM guard and a resolved protected Docker=true request.
func vmDockerProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
