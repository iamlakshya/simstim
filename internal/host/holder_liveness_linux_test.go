package host

import (
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func waitForUnreapedExit(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		var info unix.Siginfo
		done <- unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("child did not exit")
	}
}
