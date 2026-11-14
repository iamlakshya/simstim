package host_test

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// The selector names one already owned PID. An empty result means that
// child exited and was reaped. No argv or environment is requested.
func ownedHolderState(pid int) (string, bool, error) {
	if pid <= 0 {
		return "", false, fmt.Errorf("invalid owned holder PID")
	}
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", false, fmt.Errorf("inspect the owned holder: %w", err)
	}
	if len(entries) == 0 {
		return "", false, nil
	}
	if len(entries) != 1 {
		return "", false, fmt.Errorf("the owned holder query returned more than one process")
	}
	p := entries[0]
	if int(p.Proc.P_pid) != pid || int(p.Eproc.Ucred.Uid) != os.Geteuid() {
		return "", false, fmt.Errorf("the owned holder query returned a different process owner or PID")
	}
	birth := p.Proc.P_starttime
	if birth.Sec <= 0 {
		return "", false, fmt.Errorf("the owned holder has no native birth time")
	}
	return fmt.Sprintf("%d:%d", birth.Sec, birth.Usec), p.Proc.P_stat != 0 && p.Proc.P_stat != 5, nil
}
