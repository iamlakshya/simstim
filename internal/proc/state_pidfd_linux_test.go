//go:build linux

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOriginalChildPidfdStopContinueAndUnreapedExit(t *testing.T) {
	fd := -1
	cmd := exec.Command("/bin/sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &fd}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	defer func() {
		if !joined {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	if fd < 0 {
		t.Skip("the kernel supplies no atomic child pidfd; this native observation is unavailable")
	}
	await := func(want ChildPidfdStateV2) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			state, err := ObserveChildPidfdV2(fd)
			if err != nil {
				t.Fatal(err)
			}
			if state == want {
				return
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("original native child state = %v, want %v", state, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGSTOP, nil, 0); err != nil {
		t.Fatal(err)
	}
	await(ChildStoppedV2)
	// Repeated WNOWAIT observations must not consume the original stop.
	await(ChildStoppedV2)
	if err := unix.PidfdSendSignal(fd, unix.SIGCONT, nil, 0); err != nil {
		t.Fatal(err)
	}
	await(ChildContinuedV2)
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil {
		t.Fatal(err)
	}
	await(ChildExitedV2)
	await(ChildExitedV2)
	_ = cmd.Wait()
	joined = true
	if state, err := ObserveChildPidfdV2(fd); state != ChildNoEventV2 || !errors.Is(err, unix.ECHILD) {
		t.Fatal("a reaped child became a fresh native state", state, err)
	}
}

func TestChildPidfdObservationRejectsNonChildAndOrdinaryFile(t *testing.T) {
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Skip("this kernel supplies no self pidfd")
	}
	defer unix.Close(fd)
	if state, err := ObserveChildPidfdV2(fd); state != ChildNoEventV2 || !errors.Is(err, unix.ECHILD) {
		t.Fatal("a non-child supplied child pause/exit observation", state, err)
	}
	file, err := os.CreateTemp(t.TempDir(), "ordinary-file-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if state, err := ObserveChildPidfdV2(int(file.Fd())); state != ChildNoEventV2 || err == nil {
		t.Fatal("an ordinary file supplied a native child state", state, err)
	}
}
