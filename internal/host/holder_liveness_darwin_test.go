package host

import (
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func waitForUnreapedExit(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(3 * time.Second)
	for {
		p, err := unix.SysctlKinfoProc("kern.proc.pid", cmd.Process.Pid)
		if err == nil && p.Proc.P_stat == 5 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("child did not reach unreaped exit state")
		}
	}
}
