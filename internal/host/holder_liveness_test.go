package host

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestProcessLivenessDoesNotReapOwnedChild(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitForUnreapedExit(t, cmd)
	if pidAlive(cmd.Process.Pid) {
		t.Error("an exited child counts as alive")
	}
	err := cmd.Wait()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 7 {
		t.Fatalf("liveness stole the owning wait: %v", err)
	}
}

func TestRecordedHolderRejectsOtherCommand(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("the test process is not visible")
	}
	if holderPIDAlive(os.Getpid(), "abcd2345") || holderPIDAlive(1, "abcd2345") {
		t.Fatal("an unrelated process counts as a recorded holder")
	}
	if os.Geteuid() != 0 && pidAlive(1) {
		t.Fatal("another user's process counts as owned")
	}
	args, err := holderProcessArgs(os.Getpid())
	if err != nil || len(args) == 0 || args[0] != os.Args[0] {
		t.Fatalf("read own process argv: %q, %v", args, err)
	}
}
